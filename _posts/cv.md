---
title:  "CV"
search: true
categories:
  - Jekyll
  - Go
  - codes
  - src
last_modified_at: 2025-08-03T03:06:00-05:00
---
<table style="width:100%;">
  <tr>
    <td style="vertical-align:top; padding-right:20px;">

# XXX

<div>
  <span>
    <img src="assets/phone-solid.svg" width="18px"> 15988788890(微信同号)/18868846460
  </span>
  ·
  <span>
    <img src="assets/envelope-solid.svg" width="18px"> issacpan98@gmail.com
  </span>
  ·
  <span>
    <img src="assets/images/Snipaste_2025-07-10_23-10-14.png" width="18px"> [CyC2018](https://github.com/issacpan1998)
  </span>
  ·
</div>

```
</td>
<td style="width:140px; vertical-align:top; text-align:right;">
  <!-- 把下面的路径替换成你的头像文件路径。建议 150x150 像素，圆形。 -->
  <img src="assets/avatar.jpg" alt="头像" style="width:140px; height:140px; object-fit:cover; border-radius:50%; border:2px solid #ddd;">
</td>
```

  </tr>
</table>

##  个人信息

* 男，1998 年出生
* 求职意向：GO开发工程师(业务方向)
* 工作经验：1 年

## 教育经历

* 研究生，浙江大学，软件工程，2021.9-2024.6
* 本科，浙江工业大学，信息与计算科学，2017.9~2021.7

##  工作经历
* **滴滴出行 实习生 基础平台-文件存储 2022.11 - 2024.05** 
 - 负责GUiGU相关平台的运用，用户如何查询文件，同时提供大量易用性接口，同时，参与MDS模块的开发
 - 参与MDS模块的建设，例如参与
* **滴滴出行 后端开发工程师 基础平台-文件存储 2024.07 - 2025.10** 
### 滴滴Orangefs分布式文件系统
  - 实习期间，主要参与GUIGU及MDS的开发，
  - GUiGU     - 负责实现管控平台，用户对文件进行上传、下载以及查询，如何用户如何查询文件， 同时提供大量易用性接口，同时，参与了部分MDS模块的开发
  - 正式工作期间，工作重心放在BS模块，参与了离线EC体系建设，设计全局数据校验
  - MDS模块开发: 


  参与滴滴分布式文件系统及对象存储系统的研发， 
  - 参与离线EC模块的迁移, 

  - 负责设计BS层面数据迁移/
  - 参与数据迁移
  - 参与其他易用性功能的开发


##  项目经历
* **离线EC迁移**
- 背景: 业务早期阶段，数据由多副本进行冗余存储，存在资源浪费、读取无法stripe化的问题，需要高效并可靠地将数据迁移至EC(erasure coding，纠删码)存储。
- 主要工作：负责开发离线数据迁移。由Master扫描多副本数据，将待迁移数据存放至Migrate中间表，Worker负责从中间表中挑选数据。
- 困难：选型，迁移效率及正确性问题，各任务
- 解决： 
- 思考： 其实做的并不成功，很多问题上线后才得到解决。低估了迁移的CPU占用，缺乏前后台任务限流管理。
* **BS2.0存储引擎**
- 背景: BS1.0基于小集群设计，每个集群有单独的Master节点管理数据，运维成本高且磁盘利用率低。BS2.0采用大集群设计并新增了更多数据保障功能
- 主要工作：1.设计并开发BS2.0 数据可靠性模块，检查数据副本及数据块是否健康    
-          2.设计并开发BS2.0 并发垃圾回收模块，
-          3. 优化BS2.0，内存池的运用，
- 困难:  1. 任务调度框架的设计，可能存在数十亿个数据副本，如何高效地查询，
-       2. 如何做限流，如何取得读写流量和后台的均衡?
-       3. 开发初期性能不如BS1.0，如何优化？
-       
- 解决: 1.将副本模块化，

* **MDS元存储引擎**
- 背景: 随着元数据量的增长，原有Mysql存储遇到了性能瓶颈，因此需要新的LSM Tree KV架构支持海量数据存储
- 主要工作: 讨论方案，协助研究
- 困难:  1. RAFT相关: "串行化"的优化，如何与业务场景适配;
-        减少RAFT Apply的粒度，将较重的操作放置在RAFT之前。
-       3. 
-       
- 解决: 内存事务前置检查，仅放OP。 Follower Read 

