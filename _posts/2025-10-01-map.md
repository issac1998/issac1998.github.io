---
title:  "go-map"
search: true
categories:
  - Jekyll
  - Go
  - codes
  - src
last_modified_at: 2025-10-10T03:06:00-05:00
---
Go的早期版本是依赖hashmap实现的，但在1.24版本替换成了swiss table，根据基准测试值，在大多数场景下能带来30-50%的提升

- [hashMap 实现](#hashmap-实现)
    - [hmap](#hmap)
    - [bmap](#bmap)
    - [mapextra](#mapextra)
    - [map操作](#map操作)
      - [makeMap](#makemap)
        - [makeBucketArray](#makebucketarray)
        - [newarray](#newarray)
      - [mapaccess1，mapaccess2](#mapaccess1mapaccess2)
          - [overflow计算，还得看看为什么是这么算](#overflow计算还得看看为什么是这么算)
      - [mapdelete](#mapdelete)
      - [evacuated(oldb)](#evacuatedoldb)
      - [mapassign](#mapassign)
        - [newoverFlow](#newoverflow)
        - [hashGrow](#hashgrow)
      - [growWork](#growwork)
      - [evacuate](#evacuate)
      - [iterator](#iterator)
        - [mapiterinit 迭代器初始化](#mapiterinit-迭代器初始化)
        - [mapiternext](#mapiternext)
- [SwissTable 实现](#swisstable-实现)
    - [元数据](#元数据)
    - [Probing](#probing)
      - [Probing within a group](#probing-within-a-group)
    - [Deletion](#deletion)
    - [Growth](#growth)
  - [Map struct](#map-struct)
  - [Map初始化](#map初始化)
  - [Get](#get)
  - [查询](#查询)
  - [插入和修改](#插入和修改)
  - [删除](#删除)
  - [扩容](#扩容)
    - [group数量翻倍](#group数量翻倍)
    - [Table分裂](#table分裂)
  - [遍历](#遍历)
  - [问题](#问题)
    - [引用类型和非引用类型](#引用类型和非引用类型)
    - [为什么只清除带指针的Key呢，不清除的Key怎么办？](#为什么只清除带指针的key呢不清除的key怎么办)
    - [为什么swisstable会快？](#为什么swisstable会快)
    - [skip -list和map的区别](#skip--list和map的区别)
    - [看着好像map的Key不能是Map func Slice？那二维Map怎么做？](#看着好像map的key不能是map-func-slice那二维map怎么做)
    - [新旧map都是怎么实现以下semantic的？](#新旧map都是怎么实现以下semantic的)
      - [新map](#新map)
  - [新版本swiss Map有任何帮助GC的举动吗？类似老Map标记Map没有指针， 扩容时删除旧表等操作](#新版本swiss-map有任何帮助gc的举动吗类似老map标记map没有指针-扩容时删除旧表等操作)
    - [unsafe.Pointer,uintptr](#unsafepointeruintptr)
  - [参考](#参考)

# hashMap 实现

map用hash表实现，数据被存在一组桶中，每个桶有8个 kv pairs。

key hash值的低n位用于选桶，高8位用于tophash快速定位key的位置。即一个桶中的Key，低位是相同的，n由一个桶中想存的值个数决定，默认为3。 tophash可以根据高8位值快速定位一个桶中key的位置，避免 key 本身的内容过大导致比较慢，但最终还是要比较key已避免哈希碰撞
（可以拿图）

如果bucket有超过8个key，那么chain on extra buckey

当hashtable grow时，分配新的一组桶，并**增量复制**到新的一组桶去。

每个桶中的key是不会被移动的，防止遍历时的错误。
在table grow时，遍历器不仅要遍历老的桶，如果老的桶has been moved (evacuated)到新的table，也要遍历新的table。

### hmap
hmap结构体如下
可以拿图
flags
```go
type hmap struct {
	// Note: the format of the hmap is also encoded in cmd/compile/internal/reflectdata/reflect.go.
	// Make sure this stays in sync with the compiler's definition.
	count     int // # live cells == size of map.  Must be first (used by len() builtin)
	flags     uint8 //通过flags值标志当前map的状态，例如在写时，设置hashWriting位，其他写和读根据h.flags&hashWriting != 0 判断此时有写入，并发读/写报错。。
	B         uint8  // log_2 of # of buckets (can hold up to loadFactor * 2^B items)
	noverflow uint16 // approximate number of overflow buckets; see incrnoverflow for details
	hash0     uint32 // hash seed

	buckets    unsafe.Pointer // array of 2^B Buckets. may be nil if count==0.
	oldbuckets unsafe.Pointer // previous bucket array of half the size, non-nil only when growing
	nevacuate  uintptr        // progress counter for evacuation (buckets less than this have been evacuated)

	extra *mapextra // optional fields
}
```
其中buckets指针指向bmap的数组
### bmap 
bmap 存储每个bucket的具体信息，其在编译期间扩充，包含
1.tophash记录所有key的top byte。If tophash[0] < minTopHash, tophash[0] is a bucket evacuation state instead.（问问AI）
2.记录bucketCnt个Key
3.记录bucketCnt个Value。Key和value分开记录是为了尽可能防止padding
4.记录OverFlow，可能是指针，也可能是uintptr的地址。取决于KV是否包含指针，若不含指针，为了减少GC扫描的对象，仅保存overflow uintptr的地址。将Overflow桶通过hmap的mapextra保护

### mapextra
mapextra记录overflow、oldoverflow的bmap地址，这是Go map对GC的优化。不含指针的map被特殊处理（GC不会扫描这个桶，在evacuate时会直接调用memclr）

对于正常的桶，溢出桶的指针是保存在当前桶的overflow字段中的。当map不含指针时，golang 在编译期间会把 k-v 都不含指针的 map 中的 bmap.overflow 字段优化为 unitptr 类型， 中的 overflow 字段从 *bmap（指针类型）优化为 uintptr（非指针类型），让 GC 认为整个 bucket 不含指针，跳过扫描。但这样会导致溢出桶失去 GC 可达性而被错误回收，为了使溢出桶仍然有被引用不被GC删除，需要将其保存在mapextra里，所以GC还是需要扫描mapextra的。


### map操作

运行时实现了 map 类型操作的所有功能，包括创建、查找、插入、删除、遍历等。在编译阶段，Go 编译器会将 Go 语法层面的 map 操作，重写成运行时对应的函数调用。大致的对应关系是这样的：

m := make(map[keyType]valType, hint) → m := runtime.makemap(maptype, hint, h)

v := m["key"]      → v := runtime.mapaccess1(maptype, m, "key")
v, ok := m["key"]  → v, ok := runtime.mapaccess2(maptype, m, "key")

delete(m, "key")   → runtime.mapdelete(maptype, m, “key”)

// v是用于后续存储value的空间的地址
m["key"] = "value" → v := runtime.mapassign(maptype, m, "key") 

for k,v := range m{} 
// 初始化 map 迭代器，后续操作以迭代器 hiter 为准
// 每次迭代会调用 mapiternext(it *hiter) 函数，返回下一个 key 和 value

// for range map 编译器源码注释
// The loop we generate:
// var hiter map_iteration_struct
// for mapiterinit(type, range, &hiter); hiter.key != nil; mapiternext(&hiter) {
//     index_temp = *hiter.key
//     value_temp = *hiter.val
//     index = index_temp
//     value = value_temp
//     original body
// }

#### makeMap
根据传入的hint（编译时最大的桶数量，需要被分配在堆上）和负载因子（固定为13/16，大约是0.8），计算出初始化时需要的最小桶数量。（这一块怎么计算的，需要问问ai）

如果最小桶数量不为0.则在makeMap时就allocate hash table，调用**makeBucketArray**分配buckets，并填充到h.buckets字段

如果**makeBucketArray**提前分配了overflow，则设置h.extra.nextOverflow

返回*hmap，引用类型


##### makeBucketArray
入参包含dirtyalloc（unsafe.Pointer），如果为nil，则调用**newarray**分配数个buckets的array，返回第一个bukcet的地址，如果不为nil，清理dirtyalloc的空间

##### newarray
根据入参n和typ 调用**mallocgc** 分配n个typ对象的array


如果需要分配的桶>=4,那么go认为发生overflow的可能性大，在分配的array后面增加几个overflow桶，并将最后一个overflow桶的指针设为第一个存储桶，这样通过判断溢出桶的 overflow 是否为 nil 就可以知道是否是最后一个空闲溢出桶。如果是最后一个空闲溢出桶，那么将 map 里面的 extra.nextOverflow 字段设置为 nil，表示预分配的空闲溢出桶用完了，后面如果再需要溢出桶的时候，就只能直接 new 一个了。

#### mapaccess1，mapaccess2
返回 a pointer to h[key],如果不存在，则返回一个reference to the zero object for the elem type（看代码是返回了unsafe.Pointer(&zeroVal[0])，怎么理解？）
返回的指针可能会使整个map存活，所以建议不要hold onto in for very long

如果h.oldbuckets 不为nil，即hmap的桶正在迁移过程中，那么如果旧桶还未完成迁移，需要从旧桶中开始查找。

从桶b开始遍历b.overflow直至nil，比较tophash->具体key（如果存的是个地址指向Key的地址而不是Key本身，需要解引用），如果找到则返回。在遍历过程中，如果tophash值等于emptyRest，后续就不需要再进行遍历了

###### overflow计算，还得看看为什么是这么算
这里再强调一下 overflow 的计算方式，uintptr(t.bucketsize)-goarch.PtrSize 得到了溢出桶字段在 bmap 的偏移量（goarch.PtrSize 为机器上一个字节的大小，也是一个指针的大小），通过寻址的方式 add(unsafe.Pointer(b), uintptr(t.bucketsize)-goarch.PtrSize) 找到了 bmap.overflow 字段的地址，进而获取 bmap 指针。

#### mapdelete
删除Key前先判断，如果正在迁移，调用**growWork**，**growWork**不仅需要先完成对应桶的迁移，还需要额外迁移一个桶。

1.首先清除Key：仅当其中有指针时才清除键。如果存的是个地址指向Key的地址，那么将其指向nil，由GC回收。如果Key本身是个指针，那么调用**memclrHasPointers**清除指针内存。

2.清除Value：和删除Key一样逻辑，但是对于非指针类型的，也调用**memclrNoHeapPointers**清除空间

3.将tophash 置为标志位 emptyOne，标志此槽为空

4.如果删除的 key 是 bucket 或其溢出桶中的最后一个有数据的元素，后续都没数据了，倒序往前遍历Key，（如果当前桶是溢出桶，还需往前遍历之前的桶），把 emptyOne 标志位都置为 emptyRest，直到有数据或链表到头。

判断是否是最后一个有数据的元素：
    a.如果i是bucket的最后一个元素，检查b.overflow==nil或b.overflow.tophash[0]==emptySet
    b.下一个tophash为emptyRest

5.h.count--，如果清空了桶，重置哈希种子，使攻击者更难重复触发哈希冲突


                        
原文链接：https://blog.csdn.net/Jeff_fei/article/details/134052696
#### evacuated(oldb)
根据tophash[0]值判断桶的状态，（下面字段要结合ai看看）
emptyRest      = 0 // this cell is empty, and there are no more non-empty cells at higher indexes or overflows.
	emptyOne       = 1 // this cell is empty
	evacuatedX     = 2 // key/elem is valid.  Entry has been evacuated to first half of larger table.
	evacuatedY     = 3 // same as above, but evacuated to second half of larger table.
	evacuatedEmpty = 4 // cell is empty, bucket is evacuated.

#### mapassign
写入和修改都会走到这个函数，和mapaccess基本一致，但会给新key分配新slot。新Key根据hash找桶，桶内遍历Slot找空闲位置。mapassign设置Key的值，返回elem的地址指针。

1.若h.buckets为空，调用mallocgc分配桶
2.类似于删除，对于正在迁移的hmap，也需要调用growwork帮助迁移

3.根据tophash找Key，如果有，调用typedmemmove更新Key。如果找不到，则需要分配新的cell:
a.如果overLoadFactor(13 / 16的cell有元素，这种情况将扩容桶的个数为原先两倍)或有太多溢出桶（重新整合桶，减少overflow数量），触发**hashGrow**，再重试上述步骤
b.如果当前桶和溢出桶都满了，则分配一个新的**newoverFlow**
c.找到插入位置，如果Key、Elem本身较大(>128K)，maptype会设置IndirectKey、IndirectElem，并调用newobject初始化Key地址，此时slot会指向分配的地址。

d.typedmemmove(t.Key, insertk, key)将Key值复制到insertK （此时InsertK可能指向槽（对于普通K），也可能指向Key的地址（>128K的key））

##### newoverFlow
1.从创建时优先分配的h.extra.**nextOverflow**拿，如果是最后一个溢出桶（他的overflow有值），则重值他的overflow为nil，并将.extra.**nextOverflow**置为nil
2.否则ovf = (*bmap)(newobject(t.Bucket))创建一个新的溢出桶
3.记录溢出桶数量，溢出桶数量小时，是精确值，否则为大概计数
4.如果桶中 key,value 不含指针（ t.Bucket.PtrBytes == 0），将桶记录h.extra.**overflow**中，防止被GC
5.设置B的溢出桶为ovf:func (b *bmap) setoverflow(t *maptype, ovf *bmap) {
	*(**bmap)(add(unsafe.Pointer(b), uintptr(t.BucketSize)-goarch.PtrSize)) = ovf
}(这里的**是什么意思)

##### hashGrow
这一步**分配了新桶和新的地址空间**，将老hmap的overflow，bucket置为oldOverflow，oldBucket。同时如果在扩容时，老bucket还在遍历，则需要更新h.flags的旧bucket遍历标志位为1，后续遍历时就从老桶遍历

具体的迁移工作是在读写时done incrementally by growWork() and evacuate().

#### growWork
在删除、新增、修改Key时、若通过h.oldBucket!=nil判断桶在迁移，则会调用growWork进行迁移
growWork选择操作的桶和一个nevacuate的桶进行evacuate扩容
nevacuate指向需要扩容的桶： progress counter for evacuation (buckets less than this have been evacuated)

#### evacuate
发生迁移时，新桶的数量要么与原先一致（overflow桶太多），要么扩容一倍（overLoadFactor）。桶是根据Key的hash低位决定的，迁移后，计算桶一位hash值。因此对于一个Key，他有可能迁移至相同编号的桶（新计入计算的），也可能迁移至新的桶。Go通过将原Key的tophash设为X\Y来标记两种情况（X：新纳入考虑的低位hash值为0，仍是这个桶序号，Y：新纳入考虑的低位hash值为1，不是这个桶序号）

对于大多数Key，X\Y其实并没有意义，因为每次都可以通过hash值计算。X\Y主要是为了NanKey，即!t.Key.Equal(k2, k2) 的情况，这种情况下Key的hash值每次都不同，所以，用迁移时tophash的最低位决定往X还是Y迁移并填入原桶tophash，为了使这类Key分散，目标桶的Hash随机计算。

*为什么Key是NanKey 还需要迁移？

1.对于一个桶中的元素，将tophash值改为EvacuateX和EvacuateY，供iter快速查找迁移到哪了。
2.遍历桶和他的overflow，将其中的元素放到目标地址。
3.对于含有指针的桶，清空bucket的k、v以及 溢出桶指针来帮助GC
4.按顺序遍历桶，更新nevacuate至第一个还未完成迁移的桶。若都完成迁移，则将oldbuckets和oldOverflow置空


这里每次迁移都会迁移一整个桶，同时growWork是写入/修改/删除调起的，不同操作间有h.flags冲突，不会存在反复被迁移的情况。读取Key时，通过tophash[0]的值判断旧桶是否完成迁移（由于写入flag的冲突，读取时旧桶只有迁移/未迁移两种状态），进而判断要去新桶还是旧桶找



#### iterator
先调用mapiterinit初始化iter，再mapiternext逐个遍历
##### mapiterinit 迭代器初始化
1.快照记录遍历开始时刻hmap.buckets为 it.buckets以及hmap.b为	it.b
2.记录hmap指针
3.若map不含指针，则需要记录it.overflow=h.extra.overflow。防止在桶增长时，overflow桶被GC回收
4.随机选择遍历开始的桶	it.bucket和slot
5.记录迭代状态至flag

it.bucket指向目前正在遍历的桶

##### mapiternext
1.首先确定it.bucket是否为it.startBucket，结束遍历
2.遍历it.Bucket
3.确定迭代器初始化时的状态，如果迭代器在扩容过程中启动，且此时扩容尚未完成。那么根据oldbucketmask()计算it.Bucket桶对应的旧桶，若旧桶未完成迁移，从旧桶h.oldBuckets遍历，并记录checkBucket = bucket
4.其他情况都从新桶it.buckets遍历,it.buckets是iterator初始化时hmap.bucket的快照
5.遍历桶，如果槽是空的，或者 key 已经迁移，则跳过。如果是第3步对应的情况，旧桶的Key可能被迁移至X/Y两个桶，**在遍历时根据checkbucket只返回属于新桶的数据**，例如当checkbucket为X桶时，根据tophash值只读桶X的数据。比较方法是重新算一遍Key的Hash，再看其是否会被分配到checkBucket。
6.根据tophash值，判断是否hash table has grown since the iterator was started，若没有，则直接取值，若有，则调用mapaccessK找key，mapaccessK是不与写冲突的access。
7.遍历桶的overflow
8.若桶的overflow为空，则遍历下一个桶。


根据iternext模型，以下两种情况可能导致遍历时不能读到最新的数据（问问AI确定下）：
1.已经完成某个桶遍历后又在对应桶插入数据
2.遍历时发生扩容，遍历指向的是老h.buckets,获取不到新的h.buckets,
# SwissTable 实现

SwissTable采用了并行探测的技术，大幅提升了探测的效率：
原有HashTable采用了链表法，需要逐个遍历溢出桶，在桶内还需要逐个tophash值。
而SwissTable选择了开放寻址法，同时设计了多个Table，每个Table内含有多个group，一个Group有8个slot和额外8字节的控制字节。现代计算机SIMD（单指令多数据）指令集允许我们一次处理 8 个字节的数据，一个Group的控制字节能被单次SIMD指令处理。

相比go1.24之前的map实现，其：

在大规模map或查询不存在元素时性能提升20%-50%;
插入和删除性能提升20%-50%;
内存上减少了0%-25%的消耗，固定大小的map不在产生额外的内存碎片。

### 元数据
每个Group配备了一个8字节的元数据，称为控制字节。其中每个字节对应组中的一个Slot位，第一位记录着该槽位的状态（空闲、已删除或使用中）。如果槽位正在使用，该字节低7位还会存储键的哈希值的低 7 位（我们称之为 h2）。

### Probing
计算 hash(key) 并将哈希值分成两部分：上部 57 位（称为 h1）和下部 7 位（称为 h2）。

h1的后N位用于筛选Group(N为Group数量)，h2用于选slot。

若当前Group没有对应的Slot，则发生了冲突，通过二次探测法决定的序列查找，直至找到了一个空值或需要的Key。序列由ProbeSeq决定，p(i) := (i^2 + i)/2 + hash (mod mask+1)，It turns out that this probe sequence visits every group ** exactly once **

#### Probing within a group
在Group中查找时通过元数据（控制字节）寻找，如果低位与h2匹配，则将其加入候选。这是 SIMD（单指令多数据）硬件支持的操作，只需一次SIMD操作就可并行完成Group中8个Slot的遍历。由于h2只取了后7位，有可能哈希冲突，需要比较候选值的Key是否相同。


### Deletion
由于Probing的终止条件时找到一个有空slot的Group，所以删除时不能直接删除slot，以免造成probing提前停止。因此删除只是将控制字节置为0，后续有插入时直接覆盖，或在grow时clean。


### Growth
由于Probing策略的限制，整个Table的所有Group需要同时增长，在增长完成前不能有读写操作，而Go采用的增量growth模式不能接受长时间的等待，因此，将Go map 拆分成多个独立的 Swiss Tables ，Go通过keyhash值**的前globalDepth个bit来选择table**。每个Table有自己的负载因子（默认7/8），增长时独立，不影响其他Table的正常运行。table首先增长Group数量，再将所有Group迁移至新的更大的Table。当Table的Group过多时，将拆分成两个更小的table。

当某个Table扩容时，globalDeath会增长，但我们其他Table不会触发扩容，就可能导致相同的一张Table有两个Index。这种情况在遍历时会发生问题，因此每个Table需要维护localDepth，在遍历时将Hash值映射至同一个Table。（例如原globalDeath为1，当表1发生扩容时，globalDeath值会采用2位，产生00，01，10，11一共4个值。而00，01都需要根据localDepth==1映射至原表0）	，具体实现是entries := 1 << (it.m.globalDepth - it.tab.localDepth),it.dirIdx += entries。同时也可以解决在split过程中不想重复列举Key也不想返回新插入的Key的问题

每个Table最多1024个slot

## Map struct
包含slot数量、hash seed、dirPtr指向Directory或Group的指针（对Key数量小于一个Slot数量的Map有优化：只用一个group，dirPtr指向该group，否则指向table指针的数组），globalDepth，Writing flag（简化成只有一个flag了）

## Map初始化
非常简单，就是初始化map和direcory:=make([]*table, dirSize)以及newTable，当Key数量hint小于一个Group的量时，不分配directory。当hint比较大时，不预先分配空间

## Get

## 查询
1.寻找table
	a.对于小于一个table Slot数量的Map，因为只用了一个group，调用getWithKeySmall 直接通过dirPtr定位至group，
	b.根据globalDepth定位对应的首个table（怎么处理localDepth呢）
2.根据h1定位首个group
3.通过matchH2过滤，在支持SIMD的情况下，编译器会通过SIMD比较。若不支持，用位运算
4.进一步比较Key是否相同，和老版本Map一致
5.判断该Group是否有空Slot，如果是，说明后续都不会有这个Key了，同样也是优先SIMD
6.遍历下一个Group，由于probe序列会逐一遍历group，因此调用probe.next()可以获取到下一个group，重复第三步

## 插入和修改
和查询大致相同，按group遍历，若group内寻找到相同的Key，需要overWrite时，需要判断Key的类型是float，String或interface时，需要调用typedmemmove覆盖，比如对于float的+0和-0 hash值相同，但需要覆盖（问问AI确定），而老版本的Map好像没有这个问题（问问AI）
如果group内没找到，则看是否有Group内是否有空的SLot，若有，则说明查询结束，将Key优先插入记录的第一个删除Key，覆盖他，若没有，则将Key插入到第一个空Slot。

如果t.growthleft(The number of slots we can still fill without needing to rehash.)<=0,则进行rehash

## 删除
也和老map一样，首先清除Key：仅当其中有指针时才清除键。如果存的是个地址指向Key的地址，那么将其指向nil，由GC回收。如果Key本身是个指针，那么调用**memclrHasPointers**清除指针内存。对于Value，则一概清理

但不同的是，老Map在删除时需要标记TopHash为emptyOne，并比较后续Key是否为emptyRest。新Map则设置控制位，如果group是满的，将控制位设置为空（直接删除），否则，标记删除（Once a group becomes full, it stays full until rehashing/resizing,如果不是标记删除，现有的遍历/查询都会崩溃）

## 扩容
rehash函数做两件事，若扩容后Table内slot还未达到1024个，则将group数量翻倍，否则将Table分裂
因为每次只扩容一小部分，且有Map维度的锁，不必担心有数据问题，代码变得非常简单。
### group数量翻倍
简单，生成一个新Table，将原有group的值逐个拷贝即可。有Map维度的锁，不必担心有数据问题
### Table分裂
同样简单，逐个遍历Value,迁移至left或right两个表中

## 遍历
随机初始化起点及当时的groupdepth状态，调用Next()不断获取下一个迭代值。遍历遵循下列顺序：
0.判断是否发生扩容，将当前遍历的dirIdx,dirOffset相应扩大，同时，改写groupdepth状态
1.Key数量小时，直接从dirPtr遍历获取。
2.不断调用nextDirIdx获取下一个table，这里根据localdepth做了扩容中遍历的适配，具体可以看localdepth定义
3.遍历Key就是找Slot中未删除的值。逐个遍历slot，返回未删除的Key。如果发生了Grow，去看新若遇到empty或deleted则通过查询相同的方式，分组寻找下一个未删除的值（主要为了在低load的时跳过大量空Key）

上述操作中，每次遍历到某个Key时，若遍历期间发生过扩容，则需要在新table尝试重新获取一遍Key，若拿不到，则大概率Key是被删除了，也不该返回这个Key。这里说的大概率，是因为还有Key不等于Key的情况（Key本身不能被LookUp，老版本map也有相同情况），此时getWithKey也拿不到Key，但可以返回老的值。
## 问题

### 引用类型和非引用类型

### 为什么只清除带指针的Key呢，不清除的Key怎么办？
当 key 不是指针时，源码中并没有对 key 的内存进行清理，但 elem 却被清理了，是不是有一点懵啊！原因这里解释一下：首先没有指针，不清理内存不妨碍 GC；被删除的 key 的位置被插入新 key 源码会使用 typedmemmove(t.key, insertk, key) 对其内存进行覆盖，所以不清理内存也是 ok 的。但为啥又把 elem 的内存给清了呢？这里我们留一个疑问，等看完下一小节，我们再来揭晓。
### 为什么swisstable会快？
主要是因为线性探测法，进行冲突探测时，以group为单位进行探测，大大降低了传统线性探测法冲突连锁反应的影响。

### skip -list和map的区别

### 看着好像map的Key不能是Map func Slice？那二维Map怎么做？
	default:
		// Func, Map, Slice, Invalid
		panic("needKeyUpdate called on non-key type " + stringFor(t))
### 新旧map都是怎么实现以下semantic的？
// Iteration is the most complex part of the map due to Go's generous iteration
// semantics. A summary of semantics from the spec:
// 1. Adding and/or deleting entries during iteration MUST NOT cause iteration
//    to return the same entry more than once.
// 2. Entries added during iteration MAY be returned by iteration.
// 3. Entries modified during iteration MUST return their latest value.
// 4. Entries deleted during iteration MUST NOT be returned by iteration.
// 5. Iteration order is unspecified. In the implementation, it is explicitly
//    randomized.

#### 新map
可能违反上述条件的情况都发生在扩容时
// We handle (a) and (b) by having the iterator keep a reference to the table
// it is currently iterating over, even after the table is replaced. We keep
// iterating over the original table to maintain the iteration order and avoid
// violating (1). Any new entries added only to the replacement table(s) will
// be skipped (allowed by (2)). To avoid violating (3) or (4), while we use the
// original table to select the keys, we must look them up again in the new
// table(s) to determine if they have been modified or deleted. There is yet
// another layer of complexity if the key does not compare equal itself. 

## 新版本swiss Map有任何帮助GC的举动吗？类似老Map标记Map没有指针， 扩容时删除旧表等操作

                        
原文链接：https://blog.csdn.net/Jeff_fei/article/details/134052696
### unsafe.Pointer,uintptr
## 参考
https://blog.csdn.net/Jeff_fei/article/details/134052696