# RAG 文档 ETL：从原始文件到可检索证据

> 适用：企业知识库、客服/RAG、合规检索、文档问答与代码库检索。本文是 `准备清单.md` §3 的 ingestion/ETL 补充，不绑定任何单一框架或向量数据库。
>
> 最后复核：2026-07-17。核心原则：**原始文件可追溯、抽取结果可复现、索引版本可切换、每个 chunk 可引用、失败不可静默。**

## 1. 先建立正确的边界

RAG 的文档 ETL（Extract–Transform–Load）不是一个同步 API handler，更不是“上传后立刻 embed”。它是异步、可重放、版本化的数据产品流水线：把不可信、格式多样的原始资料，转成带来源、权限、位置和质量证据的检索单元。

```text
Connector / Upload / Crawl
          │  immutable source + source revision
          ▼
    Validate → Extract → Normalize → Enrich → Quality gate
          │                   │
          │                   └─ parsed artifact + manifest + lineage
          ▼
Chunk / contextualize → embed → index build → verify → publish alias
                                                    │
                                                    ▼
                              retrieval → rerank → context packing → answer + citations
```

关键分界：ETL 负责产生**可检索证据（retrieval evidence）**；在线 RAG 负责根据 query、ACL 和知识库版本挑选证据并生成答案。两边用不可变的 `document_revision_id`、`chunk_id`、`index_version` 连接，而不是只靠文件名或更新时间猜测。

## 2. 输入格式决定抽取策略

| 输入 | 主要风险 | 推荐抽取与保留结构 | 关键 metadata |
| --- | --- | --- | --- |
| HTML / Wiki / 网页 | 导航、广告、动态内容、同页模板噪声、链接失效 | DOM 选择器/Readability 去 boilerplate；保留标题层级、正文、链接、canonical URL | URL、抓取时间、HTTP ETag/Last-Modified、DOM selector、heading path |
| Markdown / 纯文本 | front matter 与代码块混淆、相对链接丢失 | Markdown AST；保留 heading、列表、表格、代码 fence | repository/source URL、commit SHA、heading path、line range |
| PDF（数字原生） | 阅读顺序错乱、页眉页脚重复、双栏、字体编码 | layout-aware parser，输出 page + block + bbox；按版面而非裸文本顺序重建 | page、bbox、reading order、parser/version、原文件 hash |
| 扫描 PDF / 图片 | OCR 误读、旋转、低 DPI、手写内容 | 图像预处理 → OCR → layout/table detection；低置信页进入人工复核 | OCR engine/version、平均置信度、语言、page/bbox、图像 hash |
| DOCX / PPTX / XLSX | 文本、表格、图片、speaker notes、公式混合；表格会失去行列关系 | OOXML / 专用解析器；正文、表、图、备注分类型产出 | slide/sheet/table ID、cell range、heading、对象类型 |
| CSV / 数据库导出 | 没有语义标题、列类型漂移、巨量行、敏感字段 | schema profiling、类型/PII 检测；按实体或逻辑行组生成记录 | dataset version、schema hash、primary/business key、row range |
| 代码仓库 | 不能按固定 token 生硬切；依赖、符号、注释和版本非常重要 | language parser / AST，按 file → symbol → block；可额外抽取 import/call graph | repo、commit SHA、path、language、symbol、line range |
| 音视频 | 转写时间轴不准、说话人错分、画面信息未抽取 | ASR + diarization；按时间窗口切；必要时多模态帧描述 | media hash、start/end ms、speaker、ASR confidence、frame references |

**表格和图像不要降格为无坐标的字符串。** 表格至少要保留标题、列名、行/列坐标和单位；图像至少保留页码/坐标、OCR 或视觉描述、到原图的受控引用。面向数字问答时，表格还应产生结构化 JSON/Parquet，交给 SQL/分析工具；把所有表格塞到 embedding 通常不能可靠回答聚合与精确计算问题。

## 3. 标准 ETL 流程与每一步产物

| 阶段 | 处理 | 必须产物 | 失败或拒绝条件 |
| --- | --- | --- | --- |
| 0. 注册来源 | 创建 source、租户、ACL、connector identity、任务 ID | `source_id`、`ingestion_job_id` | 未授权来源、无 tenant/ACL、来源类型不支持 |
| 1. 接收与校验 | MIME + magic bytes、大小/页数/压缩比限制、病毒扫描、对象存储落盘 | immutable raw object、content SHA-256、manifest 初版 | 文件伪装、压缩炸弹、恶意文件、超限 |
| 2. 去重与版本判定 | 与上一成功 revision 比较 canonical content hash；判断新增、变更、删除、无变化 | `document_revision_id`、change set | 不以文件名判重；无法确认时进入隔离而非覆盖旧版 |
| 3. Extract | 路由到 parser/OCR/connector；得到 typed elements | parsed artifact（JSON/Markdown/AST） | parser crash、OCR 低置信、空提取、页数不一致 |
| 4. Transform | 清洗、去 boilerplate、Unicode/编码规范化、阅读顺序、标题层级、表格/代码/图片类型化 | normalized document、element map、quality report | 质量门禁失败；绝不静默吞掉内容 |
| 5. Enrich | language、标题摘要、实体、PII 分类、ACL 继承、citation locator、可选 contextual prefix | enriched elements、ACL snapshot、lineage | PII policy 阻断、ACL 不完整、引用位置不可定位 |
| 6. Chunk | 按元素/标题/语义边界创建 child/parent chunks；计算 chunk hash | chunk manifest、parent relation、token count | 空 chunk、超 token、覆盖率/重复率异常 |
| 7. Load | 批量 embedding、写 vector + lexical index + document store | embedding model/version、vector IDs、index build ID | 限流、部分 batch 失败、维度不匹配 |
| 8. Verify & publish | 抽样重取、ACL contract、引用可回跳、数量对账、golden query smoke test | verification report、index alias 切换记录 | 未达阈值则不发布，旧 alias 继续服务 |

推荐的最小 manifest（可存 PostgreSQL，体积大的 artifacts 放对象存储）：

```json
{
  "document_revision_id": "docrev_01J...",
  "source_id": "confluence:space/ENG/page/123",
  "tenant_id": "tenant_a",
  "content_sha256": "...",
  "raw_object_uri": "s3://rag-raw/tenant_a/...",
  "parser": {"name": "unstructured", "version": "x.y", "strategy": "hi_res"},
  "acl_version": "acl_20260717_01",
  "index_version": "kb-eng-v42",
  "status": "published",
  "lineage": {"job_id": "ing_...", "parent_revision_id": "docrev_..."}
}
```

`chunk_id` 应是稳定而可解释的 identity，例如 `document_revision_id + element locator + chunk ordinal + chunker version` 的确定性派生；内容 hash 另存，不要拿可变文本本身充当主键。这样可精准删除、重放、引用和比较，而不会因为重跑任务制造不可清理的孤儿向量。

## 4. 原始文档、派生产物与索引的存储策略

1. **Raw zone（真相源）**：对象存储保存原始 bytes，开启版本化、校验和、生命周期、区域/加密策略；仅 ETL 服务和受控审计可读。不要只留解析后的文本，否则 parser 修复、审计、争议处理和重新 OCR 都无从谈起。
2. **Parsed/curated zone**：对象存储保存 parser 的结构化 JSON、OCR 中间结果、表格 JSON、缩略图和 normalized Markdown；关系数据库保存 manifest、状态机、ACL、血缘和检索 locator。
3. **Serving zone**：向量库保存 embedding 与最小、可过滤 metadata；全文索引保存 lexical representation；document store 保存 chunk/parent 文本或指向 curated artifact 的引用。向量库不是唯一真相源。
4. **隔离区与死信队列（DLQ）**：不可信、失败或需要人工确认的文件独立存放，限制访问与自动重试；不得把“失败但可能包含旧数据”的对象混到 published zone。
5. **删除与保留**：以 source/revision/chunk 的可追溯关系执行 delete。法规或用户删除时，删除 raw、derived、vector、lexical、cache 和备份中的可寻址副本，并记录完成证据。软删除可用于短暂回滚，不能取代数据保留政策。

多租户的默认应是 `tenant_id` 同时存在于对象路径、DB 行、向量 namespace/filter、缓存 key 和审计事件中。访问控制必须在 **检索前/检索时**执行，绝不能先召回再在 prompt 前“过滤一下”。

## 5. 主流解析/ETL 工具：怎么选而不是盲选

| 工具/库 | 强项 | 适合 | 需要验证的风险 |
| --- | --- | --- | --- |
| Unstructured | 多格式 partition、layout-aware element，生态成熟 | 混合企业文档的通用基线 | `fast`/`hi_res` 质量与成本差异、复杂 PDF 的阅读顺序 |
| LlamaParse / LlamaIndex readers | 复杂 PDF、表格、图表和下游 RAG 连接器 | 文档质量优先、愿意使用托管解析 | 数据驻留、延迟/费用、parser 版本漂移 |
| Apache Tika | 广格式、本地部署、基础文本/metadata 抽取 | 需要稳定开源通用解析 | layout 与表格语义较弱，常需二次处理 |
| Docling | 文档到结构化表示、阅读顺序/表格等 | 自托管的高质量文档转换 | GPU/资源需求、语言/格式覆盖需实测 |
| PyMuPDF / pdfplumber / pdfminer.six | PDF 原生文本、页与坐标的细粒度控制 | 需要自己掌控 PDF pipeline | 多栏、扫描件、复杂表格需要额外能力 |
| OCRmyPDF / Tesseract / PaddleOCR | OCR 与可搜索 PDF | 扫描文档、中文/多语 OCR | 图像质量、表格/手写、置信度与人工校验 |
| Pandoc / python-docx / openpyxl / python-pptx | Office/Markdown 的格式特化处理 | 特定格式的可控转换 | 图、公式、批注与版式可能需补充处理 |
| Apache Airflow / Dagster / Temporal | DAG 编排、调度、重试、可视化 / durable workflow | 多阶段异步生产流水线 | 选一个做编排；不要让框架替代领域状态机 |

工具选择必须用代表性 corpus 做 benchmark：每种格式选正常、双栏、扫描、表格密集、密码/损坏、超长和多语言样本；测 extraction coverage、reading-order error、table fidelity、citation precision、平均成本/页、P95 延迟和人工修正率。开源库提供 parsing 能力，不替你解决 ACL、版本、发布、评估和删除一致性。

## 6. 失败处理：把“可重试”与“必须人工处理”分开

| 类别 | 示例 | 策略 |
| --- | --- | --- |
| 瞬时（可重试） | 429/5xx、对象存储超时、embedding provider 短暂故障 | 有界指数退避 + jitter；按 provider/tenant 限流；保持幂等 key；记录每次尝试 |
| 资源型（可降级） | OCR GPU 饱和、超大文档、队列积压 | 拆页/拆批、优先级队列、切换低成本 parser 或延迟处理；不可牺牲 ACL 与完整性 |
| 确定性输入错误 | 损坏文件、受密码保护、Unsupported MIME、压缩炸弹 | 不要盲重试；标为 `needs_action`/`rejected`，给用户可行动的错误原因 |
| 质量失败 | OCR 低置信、零正文、表格列塌缩、阅读顺序异常 | quality gate 阻止 publish；进入 review queue，可人工修正、换 parser 后重放 |
| 下游契约错误 | embedding 维度改变、schema 版本不兼容、vector write 部分成功 | 停止发布；清理或标记本 build；以 document revision + index version 幂等重跑 |
| 安全/合规 | 病毒、恶意宏、PII 政策冲突、ACL 缺失 | 隔离并告警；最小化错误日志中的原文；仅授权人员可解封 |

实现要点：

- **任务状态机而非布尔字段**：`received → validated → extracting → transformed → indexing → verifying → published`，并有 `retrying / needs_action / rejected / cancelled`。状态迁移记录输入/输出 hash、版本、时间与错误类别。
- **幂等性**：消息至少投递一次是常态。每个 stage 持有 `(document_revision_id, stage, stage_version)` idempotency key；写入使用 upsert 或 outbox，不能因重复消息重复计费或产生多份向量。
- **部分成功不可冒充全成功**：embedding batch 50 个 chunk 时失败 2 个，应明确保留 48 个完成状态与 2 个失败状态；整个 revision 未通过验证不能切换 alias。
- **重试预算与熔断**：为 job、tenant、provider 设置最大尝试和 deadline，避免故障时重试风暴；provider 连续异常时打开 circuit，积压到队列或切换已评估的备用 provider。
- **人工修正可复现**：修正规则也要版本化，产生新的 derived revision；不要手工改向量库记录而不留下 provenance。

## 7. 与在线 RAG 的接入点

ETL 结束并不意味着“直接可回答”。推荐把发布作为显式门：

```text
ETL published revision
  → build shadow index (chunks + vectors + BM25 + ACL metadata)
  → verification: count / ACL / citation / golden retrieval
  → atomic alias: kb:engineering:active -> index-v42
  → online query resolves alias once
  → retrieve with tenant + ACL + index_version filter
  → rerank → hydrate parent/document evidence → pack context
  → model answer must cite chunk_id / source locator
```

1. **索引构建与发布分离**：重建写入 shadow index；验证合格后用 alias/collection pointer 原子切换。在线请求在开始时解析一次 alias，并把得到的 `index_version` 固定到整个 trace，避免一次回答混合新旧索引。
2. **检索 unit 与生成 unit 分离**：用较小 child chunk 提高召回，随后通过 `parent_chunk_id` 或 locator hydrate 足够的父级上下文；引用仍指向精确 page/section/line/bbox。
3. **metadata 是安全与质量契约**：至少过滤 `tenant_id`、ACL principal/group、document status、index version、语言、时间范围；再按业务需要加入 product、region、classification。缺失 ACL 的 chunk 不可发布。
4. **评估闭环**：每次 parser/chunker/embedding/index 版本变更，都对 golden set 比较 extraction coverage、Recall@k、MRR/nDCG、citation accuracy、faithfulness、延迟与成本。只看最终答案分数会掩盖“解析丢页”这类根因。
5. **缓存也带版本与权限**：embedding、retrieval candidate、rerank 和最终答案缓存 key 至少含 tenant/ACL scope、knowledge-base/index version、模型/prompt 版本及 TTL；发布或权限变更要精确失效。

## 8. 高并发与横向扩展

把控制面和数据面拆开。HTTP/connector 层只认证、写 raw object、注册 job、投递消息；解析/OCR/embedding/index worker 独立水平扩容。不要让上传请求等待 OCR、向量写入或索引发布。

| 层 | 扩展方式 | 背压与隔离 |
| --- | --- | --- |
| 接收层 | 无状态服务 + 预签名对象上传 + outbox | 按 tenant 限制上传大小/QPS；文件先落对象存储再异步提交 |
| 调度层 | durable queue / workflow，按 source 或 document revision 分区 | priority queue（交互更新优先于全量 backfill）；DLQ；可见积压指标 |
| Parser/OCR | CPU/GPU worker 池，按页或文档 fan-out/fan-in | 每类 parser 单独队列/并发池，避免 OCR 抢占普通 HTML 的资源 |
| Embedding | token-aware micro-batch、异步批处理、provider 限流器 | 以 token 而非只以请求数限流；缓存文本 hash；按 tenant 配额 |
| Index writer | bulk upsert、shard/namespace 分区、build checkpoint | 吞吐与向量库 backpressure 联动；先写 shadow，后发布 |
| 查询层 | 无状态 retrieval 服务、读副本/shard | 查询与批量 ingestion 分开限流，保证线上 P99 不被回填拖垮 |

实战补充：

- 通过 content hash 先跳过未变化文档；通过 chunk hash 只重嵌入变化 chunk。改 embedding model 或 chunker 时另建 index version，不能就地覆盖。
- 页级并行适合 OCR/解析，但合并时必须保留原始 reading order；文档级并行适合 ACL、版本和发布原子性。
- 把 CPU、GPU、外部 parser API、embedding API、vector DB 分别设置 semaphore、timeout、rate limiter 和 circuit breaker；“更多 worker”无法解决下游配额。
- 观测五类 SLIs：接收→发布时延、成功/重试/DLQ 比率、解析覆盖率与 OCR 置信度、每页/每 token 成本、published 与 source 的新鲜度 lag。每个 stage trace 都关联 `ingestion_job_id`、`document_revision_id`、`index_version`，但普通日志默认只写摘要/hash，避免泄露原文。

## 9. 上线前验收与面试叙事

最低验收清单：

- [ ] PDF、扫描 PDF、DOCX、XLSX/表格、HTML、Markdown/代码至少各有正常、异常和边界 fixture。
- [ ] 每个 published chunk 都可回跳到 source revision + page/section/line/bbox，并能验证 ACL。
- [ ] 同一消息重放不重复 embedding/写入；部分失败不能发布；DLQ 可定位、可重放。
- [ ] 版本升级走 shadow build + golden retrieval + alias 切换；可以回滚；删除会覆盖 serving 和 cache。
- [ ] 有 parser/chunker/embedding/index 版本、成本、延迟、质量门禁和可查询的失败原因。
- [ ] 离线评估至少覆盖 extraction coverage、retrieval recall、citation accuracy；线上监控 freshness、失败率、P95/P99 与用户反馈。

面试可用的一段总结：

> “我把 ingestion 当作独立的数据产品，而不是上传后的同步转换。原始文件进入版本化对象存储，解析器根据格式产生保留页码、坐标、标题和表格语义的 typed elements；每个阶段都有内容 hash、版本和状态机。然后按结构切 chunk，批量 embedding 到 shadow index，做 ACL、引用和 golden retrieval 验证后才原子切 alias。线上检索固定一个 index version，先强制权限过滤，再召回、rerank、hydrate 父文档并返回可回跳引用。解析失败会按瞬时、输入、质量和安全问题分类：可重试的带预算重试，质量问题进人工队列，任何不完整 revision 都不会发布。这样质量问题能定位到文件、parser、chunker、embedding 或检索阶段，也能安全回滚和横向扩展。”

## 10. 推荐阅读

- [Unstructured documentation](https://docs.unstructured.io/)
- [LlamaParse documentation](https://developers.llamaindex.ai/python/cloud/llamaparse/)
- [Docling documentation](https://docling-project.github.io/docling/)
- [Apache Tika](https://tika.apache.org/)
- [OpenAI file inputs](https://developers.openai.com/api/docs/guides/pdf-files)
- [Gemini document understanding](https://ai.google.dev/gemini-api/docs/document-processing)

这些工具与模型 API 是实现部件，不是领域模型。无论换哪一个，都要保持本文的 revision、ACL、lineage、quality gate、evaluation 和 publish contract。
