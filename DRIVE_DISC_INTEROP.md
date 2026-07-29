# 驱动盘互通说明

本项目以 `ZztIsolation/zzz_calculator` 的公开驱动盘交换格式为唯一对外格式：

- `format`: `zzz-calculator-drive-disc-export`
- `version`: `1`
- 驱动盘数组：`driveDiscs`
- 槽位字段：`partition`
- 属性字段：`stat`、`value`，并保留可选的 `mode`、`label`、`rawValue`

配装器内部仍使用 `slot` 和大写属性类型。导入导出适配层负责双向转换，不改变配装算法。

## 支持的输入

1. `ZZZ-Scanner.Next` 的顶层数组 `export.json` / `export.partial.json`。
2. 包含 `items`、`driveDiscs`、`drive_discs`、`discs`、`data` 或 `export` 数组的扫描器包装对象。
3. `zzz-calculator-drive-disc-export` version 1 标准文件。
4. 计算器驱动盘数组或含 `driveDiscs` 的存储对象（兼容性兜底）。
5. 本配装器完整 AppState 备份和旧版内部驱动盘数组。

## 数据保留原则

计算器或扫描器提供、但当前配装器不使用的驱动盘字段会隐藏保存，不参与界面和配装计算。Go 数据层会在同一 JSON 层级保留未知字段；属性对象也采用相同规则。因此导入后即使经过保存、关闭和重新启动，再导出通用文件仍不会丢失这些字段。

`equippedBy` 与本配装器的 `discClaims` 语义不同。外部 `equippedBy` 会保存在隐藏字段中，本地方案同步不会覆盖它；通用导出时再恢复原值。

## 安全规则

- 文件先解析和完整校验，再生成导入计划，确认后一次性保存。
- 无法识别、版本错误、槽位非法、属性缺失或重复原生 ID 时拒绝导入。
- 扫描器和计算器数据采用合并导入，不删除本地库存及方案。
- 只有本配装器完整备份会替换整个 AppState，并显示单独确认提示。

协议参考：

- <https://github.com/ZztIsolation/zzz_calculator/blob/main/core/inventory-model.js>
- <https://github.com/ZztIsolation/ZZZ-Scanner.Next/blob/main/Scanning/DriveDiscExport.cs>
