# 本地浏览器依赖

| 库 | 固定版本 | 文件 | 作用 | 来源 |
|---|---|---|---|---|
| Marked | 18.1.1 | `marked.umd.js` | Markdown → HTML | [官方仓库](https://github.com/markedjs/marked)、[文档](https://marked.js.org/) |
| DOMPurify | 3.4.17 | `purify.min.js` | 清洗转换后的 HTML | [官方仓库](https://github.com/cure53/DOMPurify) |

文件来自 npm 官方注册表对应版本的发布包，以 `npm pack --ignore-scripts` 获取。页面直接加载本地副本，不从 CDN 下载；不需要在服务器安装 Node.js。

- `marked.LICENSE` 保留 Marked 的 MIT 许可证及相关版权声明。
- `dompurify.LICENSE`、`dompurify.LICENSE-MPL` 保留 DOMPurify 的 Apache-2.0 / MPL-2.0 双许可证。
- Marked 只转换 Markdown，不负责过滤 HTML；本项目必须先经过 DOMPurify 清洗，再插入页面。

发布包完整性（npm integrity）：

```text
marked@18.1.1
sha512-zEGgrtUG8crk+TrMacJSVABU4DYDDkS8uqgU3ohrn/2fW9C2AEfUrO3m6IhpIpQpl7YcVpiXXC9cSMWDANT3dg==

dompurify@3.4.17
sha512-B/pSlYVHQ58kqISD/yDjuJ1yfzI34m1gF4lAod2bwSRhiN4K5Cl3H8Af7hqB2PmTL911tdEudnnXn5FnhRoz0w==
```

升级时同时替换文件、许可证和本说明中的版本，并重新检查 Markdown 渲染、链接过滤与 CSP 兼容。
