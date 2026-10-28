# dmemd 文档

[返回项目首页](../README.md)

项目介绍与最小上手流程见根目录 README；详细使用说明、技术语义和设计契约统一在此导航。
命令与源码路径如无特别说明，均相对仓库根目录。

## 使用与运行

| 文档 | 内容 |
| --- | --- |
| [技术与使用指南](technical-guide.md) | 当前实现的完整说明与调用示例 |
| [环境要求](technical-guide.md#环境) | Go、Node、npm 与构建依赖 |
| [构建与验证](technical-guide.md#构建与验证) | 纯 Go 构建、检查与可选容量测试 |
| [管理台构建与开发](technical-guide.md#前端管理台) | 带 UI 构建、开发代理、访问方式与页面范围 |
| [配置与运行](technical-guide.md#运行) | 目录、单实例锁、模型配置、任务重试与后台治理 |
| [HTTP API 调用示例](technical-guide.md#http-api-调用示例) | 摄取、任务、召回、冲突、反馈、版本、删除和健康检查 |
| [当前限制](technical-guide.md#首版限制) | 信任与隔离边界、容量验证范围和暂不支持的能力 |

## 实现语义与架构

| 文档 | 内容 |
| --- | --- |
| [功能与实现语义](technical-guide.md#功能与实现语义) | 记忆演化、时间与冲突模型、检索和降级行为 |
| [派生质量与来源审计](technical-guide.md#派生质量与来源审计) | 主题规范化、派生资格、引用完整性和离线审计 |
| [系统 AI 记忆服务架构设计](系统%20AI%20记忆服务架构设计.md) | 整体架构依据 |
| [前端管理台架构设计](frontend-architecture.md) | 前端边界、部署、页面、状态与 API 交互 |

## 接口与协议

| 文档 | 内容 |
| --- | --- |
| [OpenAPI 契约](../api/openapi.yaml) | HTTP 接口定义；保留在 `api/`，供前端类型生成使用 |
| [接口语义决策](contracts/api-decisions.md) | 架构未展开的 HTTP 语义与边界 |
| [远端模型协议](contracts/model-provider.md) | 提取、决策、治理与 Embedding 的模型协议 |
| [存储契约](contracts/storage.md) | SQLite 连接、迁移、表与事务约束 |

## 开发与验证

| 文档 | 内容 |
| --- | --- |
| [测试与统一验证命令](testing.md) | 测试命令、隔离方式、覆盖范围、CI 与评估边界 |
| [依赖与 SQLite 能力基线](dependency-baseline.md) | 固定依赖版本、SQLite 能力与升级规则 |

## 配置与图示

- [配置模板](../configs/dmemd.example.json)：启动前替换模型端点与模型名占位值，按需填写凭据。
- [框架图（PNG）](assets/dmemd-framework.png) / [SVG 源图](assets/dmemd-framework.svg)：
  项目概念框架；虚线表示规划能力，不代表当前实现。
