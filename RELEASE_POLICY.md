# 正式版双版本发布规则

从 V1.0 起，每个功能版本固定发布两个 GitHub Release：

## A 版：精简版

- 标签格式：`vX.YA`，例如 `v1.0A`。
- 标题格式：`ZZZ Drive Optimizer VX.YA（精简版）`。
- 不显示“打开驱动盘扫描器”按钮。
- 不注册 `/api/scanner/start`。
- ZIP 不包含 `scanner` 文件夹、.NET 运行库、ONNX 模型或第三方扫描器 EXE。
- 仅包含配装器 EXE、使用说明、免责声明和 SHA256 文件。

## B 版：离线扫描器版

- 标签格式：`vX.YB`，例如 `v1.0B`。
- 标题格式：`ZZZ Drive Optimizer VX.YB（离线扫描器版）`。
- 显示绿色扫描器启动按钮并注册 `/api/scanner/start`。
- ZIP 包含通过 `SCANNER_BUNDLE.json` 校验的完整 `scanner` 文件夹。
- Release 说明必须标注扫描器上游项目、随包版本、平台限制和操作步骤。

## 共同规则

- A/B 必须由同一提交、同一数据版本和同一计算核心构建。
- A/B 使用相同 `appVersion` 状态结构，库存文件可以直接互换。
- 两个 ZIP 分别生成 SHA256 文本文件。
- 发布前必须运行 Go 测试、Go Vet、前端语法检查、角色数据库审计、数据互通测试和压缩包完整性检查。
- Release 先发布 A，再发布 B，使 B 成为默认显示的最新完整版本。
- 正式版新增数据需标明核对日期；仍为测试服数据时必须明确提示，不得写成官方最终数值。
- 仓库不提交大型扫描器运行库；扫描器只作为 B 版 Release 附件随包发布。
