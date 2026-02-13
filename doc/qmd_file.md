下面我按“它是什么”“存储逻辑”“处理逻辑”“最终效果/你能得到什么”“优缺点与适用场景”把 **tobi/qmd** 这个项目拆开讲清楚（基于仓库 README 里给出的架构、数据表、检索融合与模型配置说明）。 ([GitHub][1])

---

## 1) qmd 究竟是什么程序？

**QMD（Query Markup Documents）**是一个**本地(on-device)的文档检索引擎**，以 **CLI 工具**形式工作，面向“你的 Markdown 笔记/会议记录/文档/知识库”等纯文本内容：

* 既支持 **关键词检索（BM25）**
* 也支持 **语义向量检索（embedding）**
* 还支持 **LLM 参与的 query expansion（扩展查询）与 rerank（重排）**
  并且这些都强调“本地运行”，通过 **node-llama-cpp + GGUF 模型**在本机完成。 ([GitHub][1])

此外它还提供 **MCP server**（Model Context Protocol）接口，方便 Claude/Agent 之类工具把它当“检索插件/工具”调用，而不只是 shell 命令。 ([GitHub][1])

---

## 2) 存储（Storage）这块：它把什么存到哪？怎么组织？

### 2.1 核心存储介质：一个本地 SQLite 索引库

QMD 的索引存储在本地：`~/.cache/qmd/index.sqlite`。 ([GitHub][1])
（也受 `XDG_CACHE_HOME` 影响） ([GitHub][1])

### 2.2 主要表/结构（README 直接给了 schema 摘要）

它把“配置、原文、全文索引、向量、LLM缓存”全塞进一个 SQLite 里： ([GitHub][1])

* `collections`：你添加的“收录目录集合”，包含路径、名字、glob mask 等
* `path_contexts`：你给某个集合/路径加的“上下文描述”（例如 qmd://notes 是个人笔记）
* `documents`：文档内容与元信息（docid 是 6 位 hash）
* `documents_fts`：SQLite **FTS5** 全文索引（BM25 检索用）
* `content_vectors`：把文档切 chunk 后的 embedding 存储（hash、seq、pos，chunk 约 800 tokens）
* `vectors_vec`：向量索引（README 明确写用 **sqlite-vec** 做 vector index，key 是 hash_seq）
* `llm_cache`：LLM 相关的缓存（比如扩展查询、重排分数）

> 这套设计的关键点：**一个 SQLite 文件既是“源文档存储 + 全文倒排索引 + 向量库 + LLM缓存”**，部署/迁移都非常简单（复制一个文件即可）。 ([GitHub][1])

### 2.3 文档如何变成可检索的“存储形态”？

README 给了两条管线：**Indexing Flow** 与 **Embedding Flow**。 ([GitHub][1])

**Indexing Flow（全文索引 + 文档入库）**

1. 你指定 collection（目录）与 glob pattern
2. 扫描 markdown 文件
3. 解析标题（通常取第一段 heading 或文件名）
4. 对内容 hash，生成 docid（6 位 hash）
5. 存进 SQLite 的 `documents`，并构建/更新 `documents_fts`（FTS5） ([GitHub][1])

**Embedding Flow（向量索引）**

1. 文档切分为 chunk：**800 tokens/chunk，15% overlap** ([GitHub][1])
2. 每个 chunk 按特定格式喂给 embedding 模型（“title | text”）
3. 通过 node-llama-cpp 的 `embedBatch()` 生成向量
4. 向量 chunk 存到 `content_vectors`，并进入 `vectors_vec` 向量索引 ([GitHub][1])

---

## 3) 处理（Processing）这块：查询进来后发生了什么？

QMD 把“处理逻辑”拆成三个检索命令（速度/质量梯度很清晰）： ([GitHub][1])

* `qmd search`：只走 **BM25/FTS5**（快、关键词强）
* `qmd vsearch`：只走 **vector semantic search**（语义相似）
* `qmd query`：**混合检索 + 扩展查询 + 融合 + LLM重排**（最强但更慢） ([GitHub][1])

下面重点讲你关心的“两大块逻辑”里“处理”这一块——尤其是 `query` 的“深度检索流水线”。

---

### 3.1 `query` 的整体流水线（Hybrid Query Flow）

README 里给了非常明确的 pipeline 图与参数： ([GitHub][1])

1. **LLM Query Expansion（扩展查询）**

   * 输入用户 query
   * 生成 **2 条替代表述**（variant 1/2）
   * 并对“原始 query”给更高权重（×2） ([GitHub][1])

2. **并行检索（Parallel Retrieval）**
   对每条 query（原始+扩展），同时跑两套后端：

   * FTS(BM25 via SQLite FTS5)
   * Vector Search（向量相似）
     输出多份“排名列表”。 ([GitHub][1])

3. **RRF 融合（Reciprocal Rank Fusion）+ Top-rank bonus**

   * 用 RRF 把多路排名融合：`score = Σ(1/(k+rank+1))`，k=60 ([GitHub][1])
   * 额外“保真”机制：任何列表里排 #1 的文档 +0.05，排 #2-3 的 +0.02 ([GitHub][1])
   * 只保留 top 30 候选送去重排 ([GitHub][1])

4. **LLM Re-ranking（交叉编码/重排）**
   使用 reranker 模型对 top 30 候选做“相关性判断 + 置信度(logprob)”并输出 0~1 的分数范围。 ([GitHub][1])

5. **Position-aware Blending（位置感知混合）**
   不是“全信 reranker”，而是按 RRF 位置分段混合，避免 reranker 把明显的关键词精确命中冲掉：

   * Rank 1-3：75% RRF + 25% reranker
   * Rank 4-10：60% RRF + 40% reranker
   * Rank 11+：40% RRF + 60% reranker ([GitHub][1])

> 这套设计的思想很明确：
>
> * **RRF** 负责“多路召回 + 稳定融合”
> * **Top-rank bonus** 负责“守住强精确匹配”
> * **Reranker** 负责“语义层面的最终判别”
> * **位置混合** 负责“别让重排过拟合/误杀高置信的检索结果” ([GitHub][1])

---

### 3.2 分数怎么对齐？（Normalization & Score Meaning）

README 还说明了不同检索后端的 raw score 如何转成可融合的范围： ([GitHub][1])

* FTS(BM25)：`Math.abs(score)`（范围 0 到 ~25+）
* Vector：`1 / (1 + distance)`（0~1）
* Reranker：0~10 rating 再除以 10（0~1） ([GitHub][1])

并且给了“分数区间含义”：0.8~1 高相关，0.5~0.8 中等，0.2~0.5 一般，0~0.2 低相关。 ([GitHub][1])

---

## 4) 模型与本地推理：它到底用哪些模型干哪些活？

QMD 默认会在首次使用时下载并缓存 GGUF 模型到 `~/.cache/qmd/models/`。 ([GitHub][1])
README 里列出了 3 个模型与用途： ([GitHub][1])

* `embeddinggemma-300M-Q8_0`：生成向量 embedding（约 300MB）
* `qwen3-reranker-0.6b-q8_0`：reranking（约 640MB）
* `qmd-query-expansion-1.7B-q4_k_m`：query expansion（约 1.1GB） ([GitHub][1])

并且给了 prompt 规范：

* query embedding 用 `"task: search result | query: {query}"`
* doc chunk embedding 用 `"title: {title} | text: {content}"` ([GitHub][1])

---

## 5) 最终你能获得什么效果？（从用户/工程视角）

### 5.1 作为“个人知识库搜索引擎”

你把散落在磁盘上的 markdown 笔记、会议纪要、文档目录加进 collection：

* `qmd search`：你像 grep/fts 一样秒级找关键词
* `qmd vsearch`：你用“描述性语言”找概念相近内容
* `qmd query`：你用自然语言提问，拿到“混合召回+重排”的更高质量结果 ([GitHub][1])

### 5.2 作为“给 Agent 喂上下文的检索层”

QMD 特意提供 `--json` 与 `--files` 输出，方便 agent 先拿“结构化检索结果”，再按 docid/path 精确拉取全文： ([GitHub][1])

* `qmd search ... --json`：拿 topN 的片段/元数据
* `qmd query ... --all --files --min-score 0.4`：拿“所有超过阈值的文件列表”
* `qmd get "#abc123" --full`：再把具体文档内容喂给 LLM ([GitHub][1])

这会带来一个很现实的收益：**不需要把大量文件直接塞进 LLM context**，而是用检索把范围缩到“该看的那几段/那几篇”。（这也是它在 README 里强调的 agentic flows 价值点。） ([GitHub][1])

### 5.3 作为“本地服务”

通过 MCP：

* stdio 模式：每个 client 启一个 qmd 子进程
* HTTP 模式：`qmd mcp --http` 常驻（默认 8181），模型可以常驻 VRAM，减少重复加载开销 ([GitHub][1])

---

## 6) 我把它抽象成你要的“两大块逻辑”

### A. 存储逻辑（你可以理解成：如何把文件变成可检索数据库）

1. **集合/上下文配置**：collections + path_contexts
2. **原文与元信息入库**：documents（含 docid/hash、标题等）
3. **全文倒排索引**：documents_fts（FTS5）
4. **语义向量库**：content_vectors（chunk）+ vectors_vec（向量索引）
5. **LLM 推理缓存**：llm_cache（避免重复 expansion/rerank） ([GitHub][1])

### B. 处理逻辑（你可以理解成：query 进来怎么被“多路召回+融合+重排”）

* search：FTS5/BM25 单路
* vsearch：vector 单路
* query：扩展(LLM) → 双路检索(FTS+Vector) × 多query → RRF+bonus 融合 → top30 → rerank(LLM) → 位置混合 → 输出 ([GitHub][1])

---

## 7) 适用性、边界与“你用它时该怎么选模式”

**适合：**

* markdown 为主的个人/团队知识库（笔记、会议纪要、文档站导出的 md）
* 想给 agent 一个“可控、可解释、可本地化”的检索层（MCP/JSON 输出很贴合） ([GitHub][1])

**你会遇到的取舍：**

* `query` 质量最好，但要本地跑 3 个模型（首次下载体积约几 GB），整体更重 ([GitHub][1])
* 语义检索依赖 `embed` 的 embedding 预处理；如果文档经常变动，需要 `update/embed` 做同步维护 ([GitHub][1])

**实践建议（最“工程化”的用法）：**

* 日常快速定位：先 `qmd search`
* 描述性问题/记不住关键词：用 `qmd vsearch`
* 要给 agent 找“最相关的一组材料”：用 `qmd query` + `--min-score` + `--files/--json`，再 `get/multi-get` 拉全文 ([GitHub][1])

---

如果你希望我再“更进一步”到源码级别（比如：docid/hash 具体怎么算、chunk 的 tokenization 用什么、sqlite-vec 的索引字段怎么建、llm_cache 的 key 设计、update/cleanup 的一致性策略等），我可以继续沿着 README 提到的 `src/llm.ts`、`store.ts` 等文件去做更细的静态分析——但我这边目前从 GitHub 页面抓取源码内容会受到页面加载限制，能读到 README 的架构说明与表结构已经足够解释整体设计与两大逻辑闭环。你要是方便的话：把你关心的那几个源码文件（例如 `src/llm.ts`, `src/store.ts`, `src/qmd.ts`）内容贴出来或打包上传，我就能把“实现细节层”的分析也补齐到位。

[1]: https://github.com/tobi/qmd "GitHub - tobi/qmd: mini cli search engine for your docs, knowledge bases, meeting notes, whatever. Tracking current sota approaches while being all local"
