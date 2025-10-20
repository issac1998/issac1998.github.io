---
title:  "GC "
search: true
categories:
  - Jekyll
  - Go
  - codes
  - src
last_modified_at: 2025-10-10T03:06:00-05:00
---

- [简介](#简介)
  - [并发GC](#并发gc)
    - [插入写屏障](#插入写屏障)
    - [删除写屏障](#删除写屏障)
    - [混合写屏障](#混合写屏障)
  - [为什么不用担心内存碎片问题？](#为什么不用担心内存碎片问题)
  - [为什么不采用分代GC？](#为什么不采用分代gc)
- [模式](#模式)
- [sweepgen](#sweepgen)
- [流程](#流程)
  - [触发](#触发)
    - [定时触发](#定时触发)
    - [分配内存触发](#分配内存触发)
    - [手动触发](#手动触发)
  - [GCSTART](#gcstart)
    - [标记](#标记)
      - [标记 GCMARKER](#标记-gcmarker)
      - [GC Marker工作模式](#gc-marker工作模式)
      - [gcDrain](#gcdrain)
      - [markroot](#markroot)
        - [suspendG](#suspendg)
        - [scanStack](#scanstack)
        - [resumeG](#resumeg)
        - [markrootSpans](#markrootspans)
        - [markrootblock](#markrootblock)
        - [scanblock](#scanblock)
        - [scanobject](#scanobject)
      - [mark 堆上的job](#mark-堆上的job)
      - [写屏障 Buffer缓冲区 wbbuf](#写屏障-buffer缓冲区-wbbuf)
      - [gcw](#gcw)
      - [结束标记](#结束标记)
        - [gcMarkDone](#gcmarkdone)
        - [gcMarkTermination](#gcmarktermination)
    - [清理](#清理)
    - [清理SweepOne](#清理sweepone)
      - [sweep](#sweep)
      - [对象状态判断](#对象状态判断)
    - [清理策略](#清理策略)
      - [清理完成](#清理完成)
      - [真实清理scavenger](#真实清理scavenger)
    - [问题](#问题)
      - [GoPARK的实现原理](#gopark的实现原理)
      - [GCpercent的作用](#gcpercent的作用)
      - [gosched作用](#gosched作用)
      - [bigcache可以减少GC吗？哪种string方式最好用？\[\]byte数组好像只会被GC一次？什么意思，推荐使用\[\]byte吗？可以看看腾讯那篇文章](#bigcache可以减少gc吗哪种string方式最好用byte数组好像只会被gc一次什么意思推荐使用byte吗可以看看腾讯那篇文章)
      - [为什么还需要STW？](#为什么还需要stw)
      - [异步抢占怎么做？和安全点有什么关系？](#异步抢占怎么做和安全点有什么关系)
      - [什么时候会出现pending STW？](#什么时候会出现pending-stw)
      - [defer/panic的栈帧是怎么样的](#deferpanic的栈帧是怎么样的)
      - [为什么需要STW？在标记开始和标记结束时](#为什么需要stw在标记开始和标记结束时)
      - [publicationBarrier作用](#publicationbarrier作用)

# 简介
## 并发GC
并发GC可能将**一个已经扫描完成的黑对象指向了一个被灰色或白对象删除引用的白色对象**，导致被引用的白色对象未被标记为黑色，不该被回收但被回收。

// 三色标记的实际含义：
// 白色 = gcmarkBits 未设置（bit = 0）
// 灰色 = gcmarkBits 已设置 + 在工作队列中
// 黑色 = gcmarkBits 已设置 + 已从工作队列中取出并扫描完成

### 插入写屏障
插入写屏障（Dijkstra）的目标是实现强三色不变式，保证当一个黑色对象指向一个白色对象前，会先触发屏障将白色对象置为灰色，再建立引用.

### 删除写屏障
删除写屏障（Yuasa barrier）的目标是实现弱三色不变式，保证当一个白色对象即将被上游删除引用前，会触发屏障将自身置灰，之后再删除上游指向其的引用.

### 混合写屏障

由于屏障机制费时，**不能作用于高频操作的栈*，老版本下，

Golang 1.8 引入了混合写屏障机制，可以视为糅合了插入写屏障+删除写屏障的加强版本，要点如下：

GC标记阶段堆上分配的新对象会立即被标记为黑色


栈到栈的操作没有写屏障，因为没有必要：GC扫描一个栈时，会suspend这个G。所以栈上所有对象扫描都是一次性的。

堆上的对象正常开启屏障：将对象从栈移动到到堆的操作有插入写屏障，对象从堆移动到栈的操作有删除写屏障

Golang 1.8 引入了混合写屏障机制，可以视为糅合了插入写屏障+删除写屏障的加强版本，要点如下：

• GC 开始前，以栈为单位分批扫描，将栈中所有对象置黑

• GC标记期间，栈上新创建的对象直接置黑，栈的新对象操作的对象不开启插入写/删除写屏障

• GC标记期间，堆上新创建对象直接置黑，正常启用插入写/删除写屏障

屏障描述如下，slot是当前下游对象，ptr是新下游对象。
//     writePointer(slot, ptr):
//         shade(*slot) //删除写屏障
//         shade(ptr) //插入写屏障
//         *slot = ptr 
//

## 为什么不用担心内存碎片问题？
Golang采用 TCMalloc 机制，依据对象的大小将其归属为到事先划分好的spanClass当中，这样能够消解外部碎片的问题，将问题限制在相对可控的内部碎片当中.

基于此，Golang选择采用实现上更为简单的标记清扫算法，避免使用复杂度更高的标记压缩算法，因为在 TCMalloc 框架下，后者带来的优势已经不再明显.

## 为什么不采用分代GC？
分代算法指的是，将对象分为年轻代和老年代两部分（或者更多），采用不同的GC策略进行分类管理. 分代GC算法有效的前提是，绝大多数年轻代对象都是朝生夕死，拥有更高的GC回收率，因此适合采用特别的策略进行处理.

然而Golang中存在内存逃逸机制，会在编译过程中将生命周期更长的对象转移到堆中，将生命周期短的对象分配在栈上，并以栈为单位对这部分对象进行回收.

综上，内存逃逸机制减弱了分代算法对Golang GC所带来的优势，考虑分代算法需要产生额外的成本（如不同年代的规则映射、状态管理以及额外的写屏障），Golang 选择不采用分代GC算法.

# 模式
GC具有三种模式，默认为gcBackgroundMode，即并发GC。其他两种模式只在debug时用
‘’‘go
    gcBackgroundMode 并发GC
    gcForceMode      STW 标记，并发清扫 
    gcForceBlockMode  全流程STW
‘’‘go
GC有三种状态
‘’‘go
    _GCoff             = iota // GC not running; sweeping in background, write barrier disabled
    _GCmark                   // GC marking roots and workbufs: allocate black, write barrier ENABLED
    _GCmarktermination        // GC mark termination: allocate black, P's help GC, write barrier ENABLED
’‘’

信号量
‘’‘go
// Holding worldsema grants an M the right to try to stop the world.
var worldsema uint32 = 1

// Holding gcsema grants the M the right to block a GC, and blocks
// until the current GC is done. In particular, it prevents gomaxprocs
// from changing concurrently.
//
var gcsema uint32 = 1
’‘’go

# sweepgen 
假设当前全局 sweepGen = N：
span.sweepgen == N-2:  //  已清扫完成，可以安全分配
span.sweepgen == N-1:  //  正在清扫中，需要等待或触发清扫  
span.sweepgen == N:    //  未清扫，包含垃圾对象，不能分配(需要清扫后才能分配)
# 流程
## 触发
有三种触发方式，分配内存达到阈值/定时/手动
### 定时触发
runtime初始化时异步起的goroutine，内部执行for循环，不断等待sysmon unpark并执行GCstart
Sysmon 每次循环判断上次GC运行时间是否超过两分钟，通过injectglist的方式唤醒forcegc的goroutine

injectglist的作用是将G改为runnable。如果没有CurrentP，则放入globbal queue;若有CurrentP，则将idle P个G放入global queue，其余放入currentP的本地queue。**对于放入globalqueue的G，会调用pidlegetspinning和startm立即运行新获得的P，对于放入本地queue的G，会等待P运行**（看看ai怎么说，那什么时候运行G呢，可能需要看看P和M的逻辑，另外pidlegetspining中还有个delicate dance，也需要看看）

对于sysmon，显然不持有P，所以会将forceg放入全局队列，并wakeP

### 分配内存触发
有两处可能触发GC，mallocGC和newUserArenaChunk 
1.mallocGC在申请了空间，即 a. 申请大小大于 32KB 的大对象调用allocLarge，b.Span满触发refill调用allocSpan两种情况时，会触发gcController.trigger()并由其根据堆内存阈值是否调用GCStart,此处的堆内存阈值会在上一轮GC结束时进行设定,下文会详细说明
2.newUserArenaChunk也会根据上述条件gcStart

### 手动触发
通过GC()执行，
1.GCWaitOnMark等待上一轮GC标记结束
2.调用gcStart开启N+1轮GC
3.等待N+1轮标记完成
4.for循环直至sweepOne返回0，表示if there was nothing to sweep
每一轮for都调用**Gosched**让出当前G（为什么要让出？）
5.for循环直至IsSweepDone，第4步只是没有等待清扫的span了，但仍然可以有正在清扫的span
6.清扫已完成，publish the stable heap profile，Only do this if we haven't already hit another mark termination.（有什么用呢？）

## GCSTART
gcStart 是标记准备阶段的主流程方法

1.再次检查是否达到GC条件，获取当前m，确保不在以下情况启动GC
a. 在系统栈 (g0) 上执行
b. 持有多个
c. m禁用抢占时(主要是调用了StoptheWorld以及gc初始化时)

2.调用SweepOne清理上一轮的所有span。
3.获取GC Sema及worldSema 

4.检查所有P的mcache是否完成了flush mcache，如果mcache的sweepgen不等于mheap的sweepgen，那么就报错（问问AI，这个过期指的是什么，为什么要flush后才能清理？）

3.初始化GCBgMarkWorker，创建gomaxprocs个新的Goroutine，睡眠直至标记阶段被findRunnableGCWorker唤醒（这里需要提前创建的原因是goroutine 的创建需要在普通的 G 栈上进行，不能在 STW 期间创建 goroutine）

4.gcMarkRootPrepare 获取data和BSS的对象个数和地址，获取所有Goroutine的快照，获取allArenas的快照

4.正式调用stopTheWorldWithSema，停止所有用户 goroutine。

5.finishsweep_m确保所有span都被sweep， 和 第二步调用SweepOne有什么差别？

6.startCycle 根据gomaxprocs值*0.25算出这次GC期望的目标利用率，整数部分为需要的dedicatedWorkers，所以P一起达到这个值。小数部分为需要的fractionalUtilizationGoal，会平均分到每个P上，例如fractionalUtilizationGoal为0.25，那么每个P需要达到0.05。后续findRunabbleGCWorker会根据这个值，选择Marker的工作模式

7.更新GCCPULimiter，问问ai limiter的作用

8.若不是gcBackgroundMode模式，设置sched.disable.user，禁止用户协程被调度.

9.设置GC为Mark阶段，gcMarkRootPrepare，gcMarkTinyAllocs

10.startTheworld，第3步创建的MarkWorker开始被唤醒并发标记。



### 标记
进行标记的时机：每轮gcstart创建的bgmarkworker，分配空间时的assistG
#### 标记 GCMARKER
gcstart创建的gcBgMarkWorker会被findRunnableGCWorker唤醒，根据被唤醒时findRunnableGCWorker设定的P的状态采用不同的GC模式

#### GC Marker工作模式
由findRunnableGCWorker/findRunable找到GCWorker，并设置GC工作模式

2.findRunnableGCWorker如果需要dedicatedMarkWorkers，则设置为**dedicate**模式，这个模式不响应抢占，持续工作直到没有更多工作。 该模式首先运行不屏蔽抢占的Marker。被抢占时，将当前 P 的本地运行队列清空并迁移到全局队列，让其他 P 能够分担这些 goroutine 的执行，然后屏蔽抢占（这里屏蔽抢占只是屏蔽在marker代码中检测gp.preemt的软抢占，如果触发了栈检测或者被异步抢占了，仍然是会被抢占的。），继续运行marker，从而实现更好的负载均衡和调度公平性。

3.findRunnableGCWorker判断P的利用率是否达到fractionalUtilizationGoal（根据这个时间周期里，P跑Mark的比例计算是否达到startcycle的值），若未达到，则设置为**FractionalMode**，这个模式运行直到被抢占或达到P的利用率。

4.findrunnable若没找到可运行的goroutine，表示这个P没有事做了，可以设置为**IdleMode**进行GC Marker，只要有其他work需要做了就停止


#### gcDrain
执行具体标记工作，分为从markrootJobs拿出一个job，调用 **markroot**标记所有Root对象。每次调用前，若满足下列条件，立即退出
1.其他进程调用了forEachP/STW
2.被抢占，且不为dedicate的屏蔽抢占模式
3.idle模式下，P有其他任务等待做（runq有任务/netpoll）
4.fraction模式下，运行时长达到目标

 最后记录本轮新增工作内容到heapScanWork，后台worker的多余工作能够补偿mutator的工作债务。
#### markroot
markroot负责标记root对象，标记完成后，调用gcFlushBgCredit提交本次mark对象的数量，主要是为了解决assistG的债务（在分配内存malloc或newUserArenaChunk时会调用deductAssistCredit，如果goroutine分配完内存size后，gcAssistBytes<0了，那么需要调用gcAssistAlloc先帮助GC，这一工作叫作mutator。帮助过程中会计算需要GC的量，此时后台worker的多余工作作为BgCredit，会补偿mutator，避免mutator阻塞）

1.从markrootNext拿出一个job，调用 markroot标记所有Root对象，这些对象在gcstart的gcMarkRootPrepare函数事先标记并记录，对象如下：

**data段**（已初始化的全局变量） 和 **bss 段**（未初始化的全局变量）:每256KB一个块，分块调用**scanblock**

**终结器管理数据结构**（调用finalizer后会放在finblock结构体中以链表组织）:对每个对象调用scanblock，确保终结器系统的管理数据不被GC回收。

**死亡的G**（会保存在gFree.Stack以链表组织）：切换至systemStack，调用stackFree释放stack，然后将其放入gFree.Nostack

**spans特殊对象**：每512页一个分片，按分片调用**markrootSpans**。scan special的func指针不被销毁，scan finalized object确保他引用的对象不被销毁。

**正常的G**:调用**suspendG**暂停G，调用**scanstack**，再调用resumeG恢复

**heap mapping object**：对于分配在堆上的对象，调用**scanObject**进行扫描

##### suspendG
suspendG 阻塞等待G停止
1.若G处于Gpreempted，将其改为Gwaiting模式
2.如果G 处于 _Grunnable/_Gwaiting/_Gsyscall，suspendG 将G的状态加上 _Gscan 位，清除 preempt 标志，完成一轮抢占

3.如果G 处于 _Grunning ，suspendG 同时发起同步和异步抢占：设置 gp.preemptStop + gp.preempt + stackguard0=stackPreempt（同步抢占请求），必要时发异步信号（preemptM）。最终G会被改为 _Gpreempted，再进行第一步转成 _Gwaiting 并加 _Gscan。

这里同时发送同步和异步是为了成功条件互补
同步：需要后续出现函数调用（或未被内联的调用点）。
异步：当前位置必须是 async safe point（非 runtime、非原子/写屏障序列、有足够栈等）。

这是一个阻塞停止，因此为了避免死锁，需要将调用suspendG的G由Grunning先改为Gwaiting，否则其他marker想停止G时，会走到以上第3步的逻辑，试图抢占这个G。但由于suspendG是运行在systemstack的g0栈上，**这个G被实际“挂起”在调用 systemstack 的那个调用点**，不会响应同步/异步信号。

##### scanStack
1/首先尝试收缩栈，如果可以安全收缩（stack上所有的frame都持有精确的指针表，比如在asyncSafePoint的时候就不能安全收缩），则立即执行shirnkstack，否则在下一个同步安全点执行。
2.扫描sched.ctxt
3.逐个对g的栈帧调用scanframeWorker，当无法确定栈帧的精确布局时，使用保守扫描，所有看起来像指针的值都当作指针处理。当知道栈帧精确布局时使用精确扫描，使用位图扫描指针（这里扫描的是直接指针，对象本身就是个指针）
4.处理defer/panic
5.处理栈上对象内部包含的指针(例如[]*int这种对象)


##### resumeG
将scan位标为0，然后调用proc的ready将g放到run-next

##### markrootSpans
##### markrootblock


##### scanblock
扫描指定内存块（非堆对象），根据指针掩码找出其中的指针，并将指针指向的对象标记为灰色（加入工作队列）。指针掩码是入参，对于/defer/panic/specails，mask全为1，对于data/bss段/stack，mask为gcdatamask（编译器处理的，使用固定位图）

findobject在堆上找到对应的obj，调用greyObject标记堆对象为灰色、标记gcmarkbits为1将其视为1️仍在使用，并入队gcw做下一轮扫描

##### scanobject
scanobject专门扫描堆对象，和scanblock的差别是使用堆位图中的指针mask（从span的GCData字段中获取），同样也是由编译器在分配时处理的：小对象写在heap bit里，大对象写在header中（因为他们可能有很多指针字段，可能heap bit放不下）。运行时才生成mask，而不像data段一样使用固定位图。

#### mark 堆上的job
从gcw拿对象，对对象进行标记
1.从P本地的work buffer获取对象，若没有，则从gcw获取对象
2.若仍然没有对象，尝试刷新所有P的写屏障缓冲区，将这些缓冲的指针标记为灰色，再将新的灰色对象加入gcw。
3.若有对象，则调用scanobject标记


#### 写屏障 Buffer缓冲区 wbbuf
每个P有自己的屏障缓冲区，用于提高写操作的效率。当触发插入写屏障时，编译器会调用gcWriteBarrier将其放入本地P的写屏障缓冲区，若缓冲区已满，需要先flush。

写屏障缓冲区同时包含了插入写屏障和删除写屏障，只有在以下情况才刷新：
1. 缓冲区满了
2. GC 阶段转换
3. P 被释放
4. gcw空

#### gcw
gcw是每个P的工作缓冲区，gcWork管理所有灰色对象，他实现了生产者/消费者模型。写屏障/root discovery/扫描栈/扫描对象 会生产灰色对象加入队列中。入队时，会将其放到wbuf（注意不是上文的wbBuf）中。

gcDarin/gcDrainN会消费队列

有点像Goroutine，gcw也会有全局gcw队列/handoff/steal的机制

#### 结束标记
GCBGMarker进行一轮标记时，会增加全局的原子变量，一轮标记结束后，会检测通过nwait检测全局的标记是否结束，若是，则调用**gcMarkDone**。
##### gcMarkDone
1/gcMarkDone获取markDoneSema确保只有一个线程执行
2.调用forEachP刷新每个P的写屏障缓冲区wbbuf，刷新gcw，防止遗漏对象
forEachP确保所有P在自己的安全点执行执行了相同操作，在不停止STW的情况下实现同步
3.stop the world并再刷新一遍写屏障缓冲区，因为可能有G在forEachP和stop the world之间刷新了写缓冲区。
4.结束gc周期，进入gcMarkTermination
##### gcMarkTermination
1.切换至MarkTermination状态，这个阶段将验证mark完成，清理资源
1.切换至GCOFF状态，关闭写屏障。
3.调用gcSweep启动清扫，唤醒后台bgsweep的g
4.Start the world
5.刷新所有P的mcache，将缓存的span放入到swept队列，后续会转换为unswept等待sweep

### 清理
进行清理的时机：1.非并发清理模式下，markTermination时的gcSweep会调用sweepone直至非0。并发清理模式下，唤醒由bgsweep创建的sweep.g做清理。
2.gcstart时需要清扫上一轮次的未完成的span
3.allocLarge或cacheSpan时调用deductSweepCredit，防止GC周期结束时出现大量未扫描的页面
4.分配时，如果需要用到这个span，清理他

清理逻辑统一调用SweepOne，最终在finishsweep_m
### 清理SweepOne

从unswept的span中拿到span，清理并函数返回清理的个数。
以下摘抄自内存一章
<!-- 
heap的sweepgen每次在开始清理前+2
span的sweepgen会在span被完成清理后更改为heap.sweepgen
span在cache时会将span标记为heap.sweepgen+3，确保不会被清扫
sweep尝试清扫span时会cas将span.sweepgen+1

因此
如果sweepgen =h.sweepgen-2 那么需要清理
如果sweepgen =h.sweepgen -1 那么正在清理
如果sweepgen =h.sweepgen 那么已经清理完成，可以使用
如果sweepgen =h.sweepgen +1 那么在开始清理前就cache的span，需要被清理
如果 sweepgen =h.sweepgen +3 已经清理完成并已经被cache 

mcentral持有partial [2]spanSet、full[2]spanSet两个字段，分别存储部分分配和全部分配的span。同时，这两个字段内细分为unswept、swept两个spanSet集合。由于GC在开始清理前将gen+2，因此通过取partial[gen%2]，每次GC前unswept、swept会进行概念上的互换.

GC开始后，这些归还的Span就成为了unswept的span，需要由GC清理
清除操作会从Unswept的span集合中弹出span，并将仍在使用的span放入Swept。同样，分配Span将span放入Swept:对于runtime而言，在uncache、allocLarge时会将Span放入swept队列中，GC开始后，这些归还的Span就成为了unswept的span，需要由GC清理。
 -->



1.循环调用nextSpanForSweep从 mcentral中拿unswept的span作为等待清理的sweep，遍历在mcentral 的full 和partial的span拿来清理（见内存分配）
2.通过CAS更改sweepgen，从N-2变为N-1,获得清扫锁
3.调用span.sweep()清理过期对象

#### sweep
具体做一个span的清理
#### 对象状态判断
GO使用标记位图来跟踪对象状态，每个span有以下两个字段。
gcmarkBits: 当前 GC 周期的标记位图，1=活跃对象，0=垃圾对象（候选）
allocBits: 分配位图，1=已分配，0=空闲槽位

扫描时，若发现对象未被标记为活跃对象，则尝试进行清理，清理策略如下
### 清理策略
1.首先观察对象是否有special处理逻辑，若有，则单独处理它。

比较典型的是finalizer：对象死亡时不会立即释放，而是复活并加入 finalizer队列，在单独的 goroutine 中执行清理函数，执行完后对象才真正死亡。weakHandle：弱引用，当一个对象只有弱引用时，可以被回收，不必等待所有引用都消失才被回收。

2.遍历span中的所有对象，如果是候选垃圾（gcmarkbits=0），且对象已被分配，则需要清理。

3.清理僵尸对象：被标记但实际已释放的对象，这通常表示：

a/用户代码错误地将指针转换为 uintptr 再转回（当将指针转换为 uintptr 时，GC 不再跟踪这个引用,如果后续又将 uintptr 转回指针使用，可能指向已被回收的内存）
b.编译器 bug 或内存损坏
c.指向已释放内存的悬挂指针（指向的内存已经被释放），可能由unsafe包导致的（unsafe.Pointer将指针转化为任意类型并被引用，这时候GC并不知道对象被引用，直接清理了内存），也可能出现在Cgo中，C端释放内存没有通知goruntime，导致悬挂指针）。其他的一些情况则是用户操作导致的，例如指向已关闭 channel 的指针。


4.根据gcmarkBits计算剩下的object数，更新allocCount为此值，并将freeindex置0。

5.将allocBits赋值为gcmarkBits，gcmarkBits赋值为0。（这里就完成了对象清理，gc实行的是逻辑标记清理，后续新对象的分配可以根据新的allocBits覆盖原有内存空间。物理内存删除是在sweepdone的最后，调用scavenger做的）

5.标记sweep完成！

6.如果是个完全空的span，调用mheap.freeSpan还给堆。否则将span还给fullSwept（全满）或partialSwept（部分清除了）。

#### 清理完成
两种完成路径
1.bgsweep for循环直到没有sweep的span，然后进入睡眠，等待下次GC唤醒
2.下一轮GC完成前，for循环强制完成所有sweep工作, 检查unswept是否为空，最后调用scavenger.wake(),将scavenger.g通过inject的方式唤醒（这是个很有意思的点，通过inject而不通过ready，一个是因为wake有可能是被sysmon调用的，而sysmon没有绑定P，而ready一定要求g绑定了P。二是可以将scavenger放在全局队列，是优先级最低的方式）

#### 真实清理scavenger
gc实行的是逻辑标记清理，GC清理不涉及物理内存删除，清理出的空间可能被后续新的分配重用。而对于没有被重用的空间，会通过scavenger做物理内存删除。
scavenger有由sysmon唤醒(sweepone做完工作后设置标志位，sysmon发现并执行)和gc开始前唤醒两种方式，同时allocSpan时如果超过内存限制或堆增长阈值也会执行h.pages.scavenge。

scavenger调用mheap.page.scavenge清理n个字节的物理空间，page的清理逻辑具体可以在内存管理中看。这里的n取64Kb，经验值。设置小更容易被抢占，延迟更低，设置大利于吞吐


### 问题
#### GoPARK的实现原理
#### GCpercent的作用
#### gosched作用
#### bigcache可以减少GC吗？哪种string方式最好用？[]byte数组好像只会被GC一次？什么意思，推荐使用[]byte吗？可以看看腾讯那篇文章
#### 为什么还需要STW？
#### 异步抢占怎么做？和安全点有什么关系？
    // asyncSafePoint is set if g is stopped at an asynchronous
    // safe point. This means there are frames on the stack
    // without precise pointer information.
    看着有同步安全点和异步安全点分别，例如收缩栈只能在同步安全点进行
#### 什么时候会出现pending STW？
#### defer/panic的栈帧是怎么样的
#### 为什么需要STW？在标记开始和标记结束时
#### publicationBarrier作用
