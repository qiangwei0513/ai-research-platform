# 前端阅读索引

前端使用 HTML、CSS 和 JavaScript，由 Go 同源提供，不需要 Node.js、npm 或前端构建。完整启动后访问 `http://127.0.0.1:8080`；不要直接双击 HTML 文件。

| 文件 | 职责 |
| --- | --- |
| [index.html](index.html) | 输入、模式切换、报告、登录和历史的页面结构 |
| [assets/app.js](assets/app.js) | 提交研究、等待与进度、报告渲染、来源、刷新恢复 |
| [assets/account.js](assets/account.js) | 注册登录、JWT 会话、历史分页、任务详情 |
| [assets/events.js](assets/events.js) | SSE 解析、Last-Event-ID、重连、去重和阶段展示 |
| [assets/styles.css](assets/styles.css) | 布局、状态和响应式样式 |
| [assets/vendor/](assets/vendor/) | Marked、DOMPurify 与许可证，页面运行必需 |

推荐按 `index.html → app.js 的提交处理 → account.js 的 api/会话 → events.js → 报告渲染` 阅读。先看请求与状态变化，样式和图标可以稍后再看。

匿名提交使用同步 `/api/research`；登录后使用 `/api/tasks` 创建后台任务，再订阅自己的事件和查询报告。JWT 放在当前标签页的 sessionStorage，SSE 使用带 Authorization 请求头的 fetch，不把令牌放进 URL。

Markdown 先转换为 HTML，再用 DOMPurify 清洗后插入页面。vendor 文件虽然不是我们编写的业务代码，但删除它们会破坏报告展示与安全过滤，应保留且无需逐行阅读。

学习版已移除 Node/jsdom 测试及其依赖目录。网页运行不受影响。
