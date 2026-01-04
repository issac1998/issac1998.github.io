---
title:  "GO内存管理"
search: true
categories:
  - Jekyll
  - Go
  - codes
  - src
last_modified_at: 2025-08-03T03:06:00-05:00
---
- [定义](#定义)
  - [page 最小的存储单元](#page-最小的存储单元)
  - [mspan -Golang 内存管理的最小单元](#mspan--golang-内存管理的最小单元)
    - [结构及状态](#结构及状态)
    - [必要字段](#必要字段)
  - [mcache-每个P对小对象的缓存](#mcache-每个p对小对象的缓存)
  - [mcentral-每个mspanClass的list](#mcentral-每个mspanclass的list)
  - [fixalloc: 分配固定大小且off-heap的对象](#fixalloc-分配固定大小且off-heap的对象)
  - [mheap](#mheap)
- [分配流程](#分配流程)
  - [分配内存空间流程](#分配内存空间流程)
    - [mallocGC](#mallocgc)
      - [Tiny allocator](#tiny-allocator)
      - [从mcache拿缓存的span](#从mcache拿缓存的span)
      - [refill，从mcentral取span放到mcache](#refill从mcentral取span放到mcache)
        - [cacheSpan](#cachespan)
        - [grow，mcentral没有可用的span了，则需要从heap新分配一些span](#growmcentral没有可用的span了则需要从heap新分配一些span)
      - [mheap.alloc，从堆上分配新的span](#mheapalloc从堆上分配新的span)
        - [mheap.allocSpan](#mheapallocspan)
      - [allocLarge](#alloclarge)
  - [heap.Grow](#heapgrow)
  - [heapArena](#heaparena)
    - [物理内存的状态](#物理内存的状态)
    - [pageAlloc内存分配器](#pagealloc内存分配器)
      - [查找空页](#查找空页)
    - [Scavenging 清理](#scavenging-清理)
      - [Scavenge](#scavenge)
        - [searchAddr：开始查找的地址](#searchaddr开始查找的地址)
        - [shouldScavenge：判断Chunk是否可被清理](#shouldscavenge判断chunk是否可被清理)
        - [scavngeOne:清理具体的页](#scavngeone清理具体的页)
- [4.allocSpan](#4allocspan)
  - [Scavenge](#scavenge-1)
    - [searchAddr](#searchaddr)
  - [问题](#问题)
    - [scav怎么计算的](#scav怎么计算的)


# 定义
我们可以从两个视角来解决 Golang 运行时的堆：

I 对操作系统而言，这是用户进程中缓存的内存

II 对于 Go 进程内部，堆是所有对象的内存起源

堆是 Go 运行时中最大的临界共享资源，这意味着每次存取都要加锁,为解决这个问题，Golang 在堆 mheap 之上，依次细化粒度，建立了 mcentral、mcache 的模型用于管理内存。具体的对象将分配到mSpan中。

go的页大小为8KB，页是最小的内存单位，mspan只可以是页大小的整数倍。mspan有约70个class，用于分配（8b-32kb）的对象，每一种class都有自己的空闲对象列表，由bitmap管理。另外有额外的spanclass==0的对象，用于分配userArena（手动内存管理，与标准 GC 堆分离的内存分配区域）

堆内存不够时，向操作系统申请，申请单位为 heapArena（64M）

// OS 内存空间 (虚拟地址空间)
//     ↓
// Arena (64MB 大块) - 连续的地址空间区域
//     ↓
// Chunk (4MB/8MB) - 页面分配器的管理单元
//     ↓
// Page (8KB) - 最小的内存分配单位
//     ↓
// Object (8B-32KB) - 实际的Go对象


除了上面谈及的根据大小确定的 mspan 等级外，每个 object 还有一个重要的属性叫做 noscan，标识了 object 是否包含指针，在 gc 时是否需要展开标记。这是在调用mallocGC时根据typ确定的。

只有不含指针的对象才能分配到Tiny中（优化GC，整个tiny block 只有在所有对象都不可达时才回收，GC不用去哪扫描指针），否则会走到mallocgcSmall


## page 最小的存储单元
Golang 借鉴操作系统分页管理的思想，每个最小的存储单元也称之为页 page，但大小为 8 KB。

## mspan -Golang 内存管理的最小单元
mheap 负责将连续页组装成 mspan。mspan 大小是 page 的整数倍（Go 中的 page 大小为 8KB），且内部的页是连续的（至少在虚拟内存的视角中是这样），mspan会根据存储的对象大小分为约70种class，同等级的mspan会通过链表链接，基于bitMap 辅助快速找到空闲内存块，每个bit代表一个object块:每个 bit 对应一页，为 0 则自由，为 1 则已被 mspan 组装.同时，建立空闲页基数树索引 radix tree index，辅助快速寻找空闲页

mspan本身是off-heap的，由fixalloc分配，但他管理的对象是on-heap的。GC每次free span时，并不会直接归还操作系统，而是会尝试复用，将其放入当前P的mspancache时，下次就不需要再调用fixalloc再走一遍分配了。

### 结构及状态
每个mspan都在一个双向链表里，不管是在mheap的busy list还是在mcentral的span list。

mSpanManual:栈内存分配/User Arena/运行时使用的span，不参与GC
mSpanInUse: 堆使用的span，参与GC
mSPanFree：没有使用的span
mspan被分配时 state == mSpanInUse 或 mSpanManual 且 heapmap(i) == span for all s->start <= i < s->start+s->npages.

mspan未被分配时，mspan在heap free treap(pagealloc)中，state=mSpanFree且heapmap(s->start) == span, heapmap(s->start+s->npages-1) == span.（只有span的头尾会标记为span）

mspan未被分配时，还可能在heap scav treap(pagealloc.scav)中，此时scavenged==true

### 必要字段

mspan中有字段freeIndex标记free的object，每次分配从这个值开始扫，直到遇到一个free object（If n >= freeindex and allocBits[n/8] & (1<<(n%8)) is 0，then object is free），然后更新freeIndex值

mspan中有字段allocCache，他标记了freeIndex处的allocBits。也就是allocBits从freeIndex起的bitmap

mspan有字段freeIndexforScan，标记GC Scanner应该开始扫的位置。
mspan有字段nelems标记span中的object值
mspan的allocBits和gcMarkbits ，是指向span's mark 和allocation bit的指针 。 清理时will free the old allocBits and set allocBits to the gcmarkBits. The gcmarkBits are replaced with a fresh zeroed out memory，具体可见GC一章


另外还有 sweep generation字段用于标记GC的状态
heap的sweepgen每次在开始清理前+2
span的sweepgen会在span被完成清理后更改为heap.sweepgen
span在cache时会将span标记为heap.sweepgen+3，确保不会被清扫
sweep尝试清扫span时会cas将span.sweepgen+1

因此
如果sweepgen =h.sweepgen-2 那么需要清理
如果sweepgen =h.sweepgen -1 那么正在清理
如果sweepgen =h.sweepgen 那么已经清理完成，可以使用
如果sweepgen =h.sweepgen +1 那么在开始清理前就cache的span，需要被清理
如果sweepgen =h.sweepgen +3 已经清理完成并已经被cache 

mcentral持有partial [2]spanSet、full[2]spanSet两个字段，分别存储部分分配和全部分配的span。同时，这两个字段内细分为unswept、swept两个spanSet集合。由于GC在开始清理前将gen+2，因此通过取partial[gen%2]，每次GC前unswept、swept会进行概念上的互换.

GC清除操作sweepone会从Unswept的span集合中弹出span，并将仍在使用的span放入Swept。同样，分配Span也将其放入Swept。


清除mspan的操作sweep
1.mspan因为需要分配而被清扫，立即返回mcache以满足分配
2.如果mspan上仍然有object分配，则将其放在mcentral的free list
3.如果msapn都空闲，mspan的pages可以返回mheap，并把mspan标记为dead（如果持有P且P的mspanCache不满，可以放进mspanCache，否则直接回收）

## mcache-每个P对小对象的缓存
在alloc字段中缓存了不同spanClass的mspan链表，同时有tiny allocator的字段，用于处理小于 16B 对象的内存分配。

## mcentral-每个mspanClass的list
mcentral持有partial [2]spanSet、full[2]spanSet两个对象，分别存储部分分配和全部分配的span。同时，这两个字段内细分为unswept、swept两个spanSet集合，unswept表示等待清理的span，swept则是正在使用的span。由于GC在开始清理前将gen+2，因此通过取partial[gen%2]，每次GC前unswept、swept会进行概念上的互换.

清除操作会从Unswept的span集合中弹出span，并将仍在使用的span放入Swept。同样，分配Span将span放入Swept:对于runtime而言，在uncache、allocLarge时会将Span放入swept队列中，GC开始后，这些归还的Span就成为了unswept的span，需要由GC清理。

## fixalloc: 分配固定大小且off-heap的对象
具体内存的分配，不论是spanalloc、cacheAlloc，还是special对象的alloc，都会使用fixAlloc。

这里有一个概念是off-heap,即不使用go的堆内存，而是从系统内存直接分配一段区域。这块内存不参与GC。

主要两个接口：alloc（），free（）
free将内存放入f.list，如果开启了f.zero，则在free时调用memclrNoHeapPointers清理内存
alloc优先从f.list返回，如果没有，再通过**persistentalloc**通过sysAlloc系统调用，从系统内存分配新的off-heap的chunk：不经过GC的堆，
fixAlloc只关心分配一块Chunk，调用他的接口再通过类型断言，将其作为具体类型使用，例如mspan。

## mheap
堆由一组arenas组成，每个arena是4M（32位）或64M（64位），由对应的off-heap的heapArena存储元数据，heapArena存储了该arena拥有的span以及pageInuse、pageMarks，pageSpecail等bitmap。内存地址可以被看作是一系列arena帧。

mheap由一组arena组成，heapArena 是 mheap 向操作系统申请内存的单位
，每个 heapArena 包含 8192 个页，一个页8Kb，则大小为 8192 * 8KB = 64 MB

通过堆外分配的heapArena存储arena的元数据，heapArena 记录了页到 mspan 的映射. 因为 GC 时，通过地址偏移找到页很方便，但找到其所属的 mspan 不容易. 因此需要通过这个映射信息进行辅助.

mheap结构体中储存了三个重要信息：
1.通过Allspan记录所有mspan信息
2.通过central所有mcentral信息 
3.通过 arenas [1 << arenaL1Bits]*[1 << arenaL2Bits]*heapArena 二级页表记录heapArena


# 分配流程
## 分配内存空间流程
（1）从 P 专属 mcache 的 tiny 分配器取内存（无锁）

（2）根据所属的 spanClass，从 P 专属 mcache 缓存的 mspan 中取内存（无锁）

（3）根据所属的 spanClass 从对应的 mcentral 中取 mspan 填充到 mcache，然后从 mspan 中取内存（spanClass 粒度锁）

（4）根据所属的 spanClass，从 mheap 的页分配器 pageAlloc 取得足够数量空闲页组装成 mspan 填充到 mcache，然后从 mspan 中取内存（全局锁）

（5）mheap 向操作系统申请内存(至少1MB)，更新页分配器的索引信息，然后重复（4）.
 
不同大小的对象会有不同的分配流程
对于微对象(小于16B)执行上述全部流程
对于小对象(小于32KB)执行上述流程的（2）-（5）步
对于大对象的分配流程执行上述流程的（4）-（5）步，直接操作mheap，绕过缓存的mspan，且整个span只有一个大对象

### mallocGC
具体代码是在mallocGC中，mallocGC在分配前，需要判断是否assist GC，再进行真正的alloc信息。
根据不同大小，会调用不同函数分配
无指针的情况，（优化GC，整个tiny block 只有在所有对象都不可达时才回收，GC不用去内部扫描指针）
1. <16B mallocgcTiny()
2. 16B-32KB 调用mallocgcSmallNoscan

有指针的情况
1. 对于小对象，调用mallocgcSmallScanNoHeader，将类型信息存储在span的heap bits中
2. 对于大对象(!heapBitsInSpan(size))，调用mallocgcSmallScanHeader，将类型信息存储在对象的头部
3. 大于32KB，调用mallocgcLarge

#### Tiny allocator
结合several tiny request至同一个memory block(span)。分配新Tiny对象时，先尝试从P的Mcache中当前的tiny block分配: 调整tinyoffset，把当前block的空间返回。如果不够用，分配新的tiny block(Span)

#### 从mcache拿缓存的span
首先获取mache中对应class大小的span，调用nextFreeFast尝试从allocCache中拿到所需Object。（allocCache是allocBits从freeIndex位起的64bit）。如果空间不够，则调用nextFree从allocBits找下一个freeIndex，如果分配满了，则调用refill从mcentral分配新的span。

如果分配了内存是则上层需要决定是否GC。

#### refill，从mcentral取span放到mcache
refill：
首先将之前的span释放，进行uncacheSpan，若sweepgen为heap.sweepGen+1（说明是在当前GC阶段开始清理前就cache的span，见GC一章），则调用sweep进行清理，否则仍有剩余空间放入central.partial，无剩余空间放入central.Full

再调用CacheSpan从 mcentral 拿一批新的。
##### cacheSpan
首先需要通过以下方式获取一个span
1. 首先通过deductSweepCredit计算credit，如果不够，则调用sweepDone sweep一些unswept的heap。这里credit计算的是按比例清扫的值，目的是维持清扫进度，避免在 GC 开始时有大量未清扫的页面

2. 依次从partialSwept拿已经清理完成的span，从unsewpt的partial和full清理span并获得。

3. 调用grow从heap分配一个span

##### grow，mcentral没有可用的span了，则需要从heap新分配一些span
调用mheap.alloc分配新的span
#### mheap.alloc，从堆上分配新的span
1. 调用mheap.alloc,切换到系统栈，如果sweep还没完成，则调用h.reclaim强制清扫需要的npage数

2. 调用mheap.allocSpan

##### mheap.allocSpan
这一步需要分配一个新的span，需要分配mspan结构体和具体的物理page。需要的page数由入参npages决定。需要先找到span和物理页，找到后进入haveSpan逻辑，初始化span。

找物理Page：
1. 分配需求npages小于16页(128K)，可以尝试首先从每个P的pcache(pageCache，不是mcache)拿到可用page的base地址, 并通过P的mspancache找mspan。Pcache为空时，还会通过pageAlloc的allocTocache分配一个64页（512KB）的对齐块，通过位图管理这64页的分配状态。
2. 其他分配，调用pageAlloc的allocTocache分配npages页。如果不够分配，调用mheap.grow(npage)首先增加至少npage的内存

找Span：

从P的**mspanCache**分配：调用allocMSpanLocked分配mspan对象，如果有P，从P的**mspancache**获取，不够则调用(*mspan)(h.spanalloc.alloc()) refill。如果没有P，直接调用(*mspan)(h.spanalloc.alloc())，最后会走到fixalloc的alloc界面

拿到Span:

这个span有可能是还需要清理的旧span。对象根据传入的参数决定是在分配时进行清理还是由调用者清理。若在分配时进行清理，小对象立即进行清理，同时为了避免大对象清理过慢，采取分块的模式进行清理，且在不同块清理期间允许被抢占。

由于上一步分配了一些scav，所以可能需要清理空间以满足memory limit和heap space limit，调用pages.scavenge来清理。

#### allocLarge
mallocLarge 绕过 mcache 和 mcentral缓存的span，直接调用 c.allocLarge
() 获取一个完整的 span。先获取mcache，再通过cache.allocLarge分配堆空间，流程大致相同。



## heap.Grow
当前Arena空间充足，则在当前Areaa中增长
当前Arena空间不够，则sysAlloc分配新的Arena


## heapArena
heapArena 的spans 字段 maps from 虚拟地址页ID to *mspan。
 -对于已分配的span，所有pages maps to span itselt
 -对于free span，只有最低和最高的pages map to the span itself，中间的pages map to an arbitrary span.
 -对于没有allocate的page，spans entries are nil.

heapArena 的pageInUse 字段 是一个bitmap，指明哪些span是在mspanInUse状态的，只有span的第一个页会被作为bitmap的索引值

pageMarks字段是一个bitmap，指明哪些span有marked object

pageSpecials字段是一个bitmap，指明哪些span有specials(finalizers or other)

zeroedBase记录了这个未被使用的第一个页的第一个byte，用于决定allocation是否需要be zeroed

### 物理内存的状态
// 内存的三种状态：
// Reserved: 已向 OS 申请地址空间，但未提交物理内存
// Prepared: 已提交物理内存，可以安全访问
// Released: 已告知 OS 可以回收物理内存（但地址空间仍保留）



### pageAlloc内存分配器
每个chunk管理4M（每个page 8K，512个page）空间，用一个512位的Bit来表示地址。0表示span未被分配，1表示span被分配。Go语言中通过pallocData保存chunk信息，pallocData是包含pallocBits（是否被分配）和ScavageBits（是否需要回收）的结构体。

PageAlloc通过chunks字段保存了chunk的二维数组，数组每个元素为pallocData

pallocSum是一个64位的uint64，表示chunk的分配信息。包含start、max、end
start表示chunk(或数个chunk，见下一段)开头有几个0，end表示末尾有几个0，max则表示这段空间有几个0

pallocSums可以表示成树的形式，底层pallocSum用于表示span是否被分配，非底层pallocSum每一个信息则表示下层的8(2^3)个pallocSum中的start、max、end信息，这里每一层选择8个Summary是因为8个Summary占用64byte空间，大概等于L1 cache line width。这个树共有5层，因此顶层可管理2^9*（2^(3)^4）=2^21个chunk，管理2^21*2^13=2^34即16G空间。

PageAlloc结构体中有Summary基数树[summaryLevels][]pallocSum，summaryLevels为5，表示共有五层，第一层的[]pallocSum由summaryL0Bits决定，通过下列公式计算得到，其中heapAddrBits是与操作系统相关的可变值，他的大小表示堆地址可以表示的空间，，在Arm64及大多数架构上，都设置为48位，因此Go可以管理2^48即256T堆空间。此时summaryL0Bits为14，即第一层有2^14个PallocSum
	// summaryL0Bits + (summaryLevels-1)*summaryLevelBits + logPallocChunkBytes = heapAddrBits

#### 查找空页
 mheap 寻页时，调用Find函数自顶向下. 对于遍历到的每个 pallocSum，先看起 start 是否符合，是则寻页成功；再看 max 是否符合，是则进入其下层孩子 pallocSum 中进一步寻访；最后看 end 和下一个同辈 pallocSum 的 start 聚合后是否满足，是则寻页成功.

 是否符合只看pallocBits（是否被分配）位，ScavageBits可以不看

PageAlloc还保存有searchAddr，可以从指定的pallocsum开始寻找。
### Scavenging 清理
回收位图与分配位图拥有一样的结构，标识该位是否被回收。回收器会从分配位图和回收位图中获取位，并对两者执行按位或运算，以确定哪些页面是 “可回收的”。然后，它会在一次系统调用中回收所有找到的连续空闲页面，并在回收位图中标记相应的位。与分配器类似，它会有一个提示地址，以避免反复遍历堆的相同部分。1表示可以已经清理或仍在用，0表示可清理


#### Scavenge
Scavenge首先获取P（后续需要pp.limiterEvent控制GC 使用率）调用h.pages.scavenge清理。

##### searchAddr：开始查找的地址
 searchAddr存储了最高的可以被清理的基地址，分为searchAddrBg或searchAddrForce
 
1.根据force参数选择searchAddrBg（由gc更改）或searchAddrForce（每次free page都更改）作为起始地址，由高地址向低地址（新分配的内存通常在高地址
，高地址的内存更可能是碎片化的，优先回收高地址内存有助于减少地址空间碎片）找到可能可以清理的Chunk和Page。

searchAddrBg 仅在每次GC新Gen中增加，主要用于后台清理程序和堆增长清理。searchAddrForce 会随着内存释放而持续增加，主要用于急切的内存回收（例如 debug.FreeOSMemory，会走到scavengeAll）和清理程序（例如分配时gcController.memoryLimit超限制，以维持内存限制）

当更高的基地址被分配时，将searchAddr设为基地址的负值，并发调用的find就会因为**不能递减searchAddr而不会清理刚分配的基地址**，下一次find时，将负值变为正值，就可以继续寻找了。

##### shouldScavenge：判断Chunk是否可被清理
a.chunk非空
b.若force（对应上述急切的内存回收）则立刻判断可清理
c.若chunk.gen（chunk的gen）==currgen(清理器的gen)，只有当sc.inUse和sc.lastInUse 都小于阈值(97.5%的页被使用)时，才可清理
d.其他情况，只需判断sc.inUse小于阈值，就可清理（和c步骤一起看看，为什么啊？）

##### scavngeOne:清理具体的页
选择到chunk后，更新searchAddr，并返回可清理的chunk，切换到G0栈（为什么很多函数都有切换到G0？）执行p.scavngeOne。主要逻辑就是调用            sysUnused(unsafe.Pointer(addr), uintptr(npages)*pageSize)告诉OS对应页标记成不可用，例如在Linux下就是madvise(MADV_DONTNEED)



https://mp.weixin.qq.com/s?__biz=MzkxMjQzMjA0OQ==&mid=2247483971&idx=1&sn=409fbc90cd37cd9856f470a0db884218&chksm=c10c4c9df67bc58b56d97526dd310a6aa946402c97cc2246cbaffc0be4ee01737f53637e11f5&cur_album_id=2782506153607118852&scene=189#wechat_redirect






2.pageAlloc页分配器
每个chunk管理4M（每个page 8K，512个page）空间，用一个512位的Bit来表示地址。0表示span未被分配，1表示span被分配。Go语言中通过pallocData保存这个信息，pallocData是包含pallocBits和ScavageBits的结构体。

PageAlloc通过chunks字段保存了chunk的二维数组，数组每个元素为pallocData

pallocSum是一个64位的uint64，表示chunk的分配信息。包含start、max、end
start表示chunk(或数个chunk，见下一段)开头有几个0，end表示末尾有几个0，max则表示这段空间有几个0

pallocSums可以表示成树的形式，底层pallocSum用于表示span是否被分配，非底层pallocSum每一个信息则表示下层的8(2^3)个pallocSum中的start、max、end信息，这里每一层选择8个Summary是因为8个Summary占用64byte空间，大概等于L1 cache line width。这个树共有5层，因此顶层可管理2^9*（2^(3)^4）=2^21个chunk，管理2^21*2^13=2^34即16G空间。

PageAlloc结构体中有Summary基数树[summaryLevels][]pallocSum，summaryLevels为5，表示共有五层，第一层的[]pallocSum由summaryL0Bits决定，通过下列公式计算得到，其中heapAddrBits是与操作系统相关的可变值，他的大小表示堆地址可以表示的空间，，在Arm64及大多数架构上，都设置为48位，因此Go可以管理2^48即256T堆空间。此时summaryL0Bits为14，即第一层有2^14个PallocSum
    // summaryL0Bits + (summaryLevels-1)*summaryLevelBits + logPallocChunkBytes = heapAddrBits


 mheap 寻页时，调用Find函数自顶向下. 对于遍历到的每个 pallocSum，先看起 start 是否符合，是则寻页成功；再看 max 是否符合，是则进入其下层孩子 pallocSum 中进一步寻访；最后看 end 和下一个同辈 pallocSum 的 start 聚合后是否满足，是则寻页成功.

PageAlloc还保存有searchAddr，可以从指定的pallocsum开始寻找。


Scavenging 清理

回收位图与分配位图拥有一样的结构，标识该位是否被回收。分配时统一将该位设为回收，不作额外操作。回收器会从分配位图和回收位图中获取位，并对两者执行按位或运算，以确定哪些页面是 “可回收的”。然后，它会在一次系统调用中回收所有找到的连续空闲页面，并在回收位图中标记相应的位。与分配器类似，它会有一个提示地址，以避免反复遍历堆的相同部分。目前是在回收器持有锁的情况下遍历的，按文档说后续可能会改成并发。1表示可以已经清理或仍在用，0表示可清理

回收每次清理几页呢？有没有优先清理大页的想法？现在好像是反向遍历清理？

3.heapArena
组装页，并记录页到Span的映射
每个heapArena共有8192个页，大小为64MB，mheap向操作系统申请内存的单位就是heapArena

type heapArena struct {
    // ...
    // 实现 page 到 mspan 的映射
    spans [pagesPerArena]*mspan


    // ...
}

# 4.allocSpan

优先从P的cache分配，如果没有，再从Heap加锁分配
1.首先从每个P的pageCache分配，避免抢锁，若Cache为空，则先调用allocToCache分配，再调用cache.ALLoc分配base和scav，分配到Base后调用tryAllocMspan不加锁的获取span，可能失败（发生在P的Cache为空时，不过不明白什么情况P的cache会空啊），失败返回空span。（这一步具体是怎么做的，我看到通过       base, scav = c.alloc(npages)分配了一个地址，如果地址不为0，则tryAllocMspan，那么Mspan是在 c.alloc(npages)分配的还是在tryAllocMspan分配的？）
2.NeedPhysPageALign 只在某些平台使用，Linux、Windows不用他，但可以问问AI他是什么用的
3.调用PageAlloc的alloc函数，分配base和scav，如果未分配到，选到用heap.grow()，再尝试分配base和Scav
4.调用allocMspanLocked加锁获取Span，加锁从P的mspanCache拿，若没有，需要调用(*mspan)(h.spanalloc.alloc())分配新的Span并填充P的Cache


可以看到allocSpan从pageAlloc.alloc和cache.alloc分配基地址和scav（拿到之后怎么用呢？），再通过heap.allocspan.tryAllocMSpan（从cache）或heap.allocMspanLocked（加锁从Heap）获取Span

5.找到Span后，看分配完之后MemroyLimit和heap-growth scavenging是否超出设定的限额，若超出限额，需要清除这部分空间（为什么只清理这部分，不多清理一点？）

a. MemoryLimit目标为gcController.memoryLimit设置的值，若gcController.mappedReady(表示Ready状态的虚拟内存)+scav大于目标，则Scavenge
b. HeapLimit 仅在第3步调用heap.Grow分配了Growth后才会计算，目标为gcPercentGoal设置的动态值，若heapRetained(heapInuse和heapFree的求和值，表示current heap RSS)+growth大于目标，则Scavenge

## Scavenge
Scavenge首先获取P（后续需要pp.limiterEvent控制GC 使用率）调用h.pages.scavenge清理。

1.根据force参数选择searchAddrBg或searchAddrForce作为起始地址，向低地址找到可能可以清理的Chunk

searchAddrBg 仅在每次新生代中增加，主要用于后台清理程序和堆增长清理。searchAddrForce 会随着内存释放而持续增加，主要用于急切的内存回收（例如 debug.FreeOSMemory，会走到scavengeAll））和清理程序（例如gcController.memoryLimit超限制，，以维持内存限制。（具体问问AI）

### searchAddr
存储了最高的可以被清理的基地址，当更高的基地址被分配时，将searchAddr设为基地址的负值，并发调用的find就会因为不能递减searchAddr而不会清理刚分配的基地址，下一次find时，将负值变为正值，就可以继续寻找了。（问问AI）

2.shouldScavenge判断按以下逻辑该Chunk是否可被清理
a.chunk非空
b.若force（对应上述急切的内存回收）则立刻判断可清理
c.若chunk.gen（chunk的gen）==currgen(清理器的gen)，只有当sc.inUse和sc.lastInUse 都小于阈值(97.5%的页被使用)时，才可清理
d.其他情况，只需判断sc.inUse小于阈值，就可清理（和c步骤一起看看，为什么啊？）

2.选择到chunk后，更新searchAddr，并返回可清理的chunk，切换到G0栈（为什么很多函数都有切换到G0？）执行p.scavngeOne



https://mp.weixin.qq.com/s?__biz=MzkxMjQzMjA0OQ==&mid=2247483971&idx=1&sn=409fbc90cd37cd9856f470a0db884218&chksm=c10c4c9df67bc58b56d97526dd310a6aa946402c97cc2246cbaffc0be4ee01737f53637e11f5&cur_album_id=2782506153607118852&scene=189#wechat_redirect

## 问题
notinheap是什么用的，看见很多他的类型转换

### scav怎么计算的
通过计算范围内chunk.scavenged为1的数量，分配时会调用pageAlloc.allocRange标记一个范围内的地址为分配，
他通过地址找到chunk，做两件事情，1.统计chunk.scavenged为1的数量 (未被回收的页的数量) 2.调用chunk allocRange将pallocBits（分配位）置1，并将scavenged置0。

这里有个疑问，为什么scavenged会有1？未被回收的话，分配器怎么会指向这个地址呢？


