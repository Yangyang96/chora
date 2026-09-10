# 依赖元数据预检

[English](dependency-metadata.md) | [简体中文](dependency-metadata.zh-CN.md) |
[文档索引](README.zh-CN.md)

产品与依赖图仍在变化时，这项预检用于保证后续 SBOM 工作持续可执行。它只在内存中
验证生成方法，不会写入或批准最终 SBOM 与第三方许可证声明。

## 当前开发身份

私有的根 npm Workspace 使用版本 `0.0.0`，以便生成符合标准的软件包标识。这是尚未
发布的开发占位符，不是 Chora 第一版公开版本。`private: true` 继续阻止意外发布到
npm，许可证元数据保持为 `AGPL-3.0-only`。

## 运行预检

```sh
npm run dependency-metadata:test
npm run dependency-metadata:check
```

检查不会安装依赖，会关闭 Go Proxy 与 Sumdb 访问，并设置 `GOWORK=off`，避免父目录或
外部 Workspace 改变清单。它会：

- 使用已安装的 npm CLI，根据 `package-lock.json` 在内存中生成 CycloneDX Application
  SBOM；
- 要求根包身份为 `chora@0.0.0` 和 `pkg:npm/chora@0.0.0`；
- 根据本地 Module Cache 与已提交的 Module 文件生成只读 Go Module 清单；
- 只打印数量与 Schema 身份，不输出完整的候选派生文档。

这不是最终跨生态 SBOM。候选冻结后，维护者仍需选择并固定最终 Generator，覆盖 npm
与 Go 依赖，收集适用的许可证文本与 Notice，人工审查例外，并保留与精确候选 Digest
绑定的输出。

## 公开候选 Dry Run

```sh
npm run publication:export:test
npm run publication:export:dry-run
```

Dry Run 会通过基于目录描述符且不跟随符号链接的写入，把已检查候选复制到私有暂存树，
再以排他方式发布目标；随后确认导出期间路径集合与源码字节/Mode 保持稳定，计算确定性
的 SHA-256 内容身份，并删除临时目录。需要保留副本供人工检查时，可以传入一个父目录
已存在的全新目标：

```sh
node e2e/publication-candidate-export.mjs /absolute/new/candidate-directory
```

Exporter 会拒绝已经存在的目标，以及源码仓库内部的目标。Dry Run 通过只是准备证据，
不代表最终候选验收或发布授权。
