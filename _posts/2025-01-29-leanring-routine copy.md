---
title:  "Learning routine"
search: true
categories:
  - Jekyll
  - Go
  - codes
last_modified_at: 2025-01-28T08:06:00-05:00
---
对象整理的优势是解决内存碎片问题以及“允许”使用顺序内存分配器。 但 Go 运行时的分配算法基于 tcmalloc，基本上没有碎片问题。 并且顺序内存分配器在多线程的场景下并不适用。 Go 使用的是基于 tcmalloc 的现代内存分配算法，对对象进行整理不会带来实质性的性能提升。
