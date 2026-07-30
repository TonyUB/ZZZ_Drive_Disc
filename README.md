# ZZZ Drive Optimizer

《绝区零》驱动盘库存管理与基础面板配装器。正式版从 V1.0 开始同时提供 A、B 两个版本，两者使用相同库存格式和计算核心。

## 下载版本

| 版本 | 内容 | 适合用户 |
|---|---|---|
| V1.05A 精简版 | 仅配装器，不显示扫描器入口，不附带扫描器运行库 | 手工录入、图片 OCR 或导入已有 JSON；希望下载体积最小 |
| V1.05B 离线扫描器版 | 配装器 + ZZZ-Scanner.Next 1.0.45 Windows x64 自包含离线包 | 希望直接从配装器启动扫描器并批量导入库存 |

两个版本可以交替使用同一份库存。内部状态结构版本继续为 `121`，不会因为 A/B 版本切换而要求迁移数据。

## 当前功能

- 手工录入、图片 OCR、扫描器 JSON、计算器 JSON 和完整备份导入。
- 紧凑库存卡片网格及传统表格管理视图。
- 盘盒图、左上槽位、右上本地占用代理人 Q 版头像、四行副属性。
- 套装、槽位、属性、代理人占用等库存筛选。
- 角色基础面板、音擎、驱动盘主副属性、2件套静态效果和组合优化。
- 方案保存、占用冲突提示、单盘归属转移和释放。
- 与 `ZztIsolation/zzz_calculator` 的通用驱动盘 JSON 往返兼容。

本软件是基础面板配装器，不是完整战斗伤害计算器。依赖动作、层数、敌人状态或队伍条件的增益只作为实战参考展示。

## 3.1 数据

当前数据库包含蕾米埃尔·丹、专属音擎“空羽复归之诗”、驱动盘“谶羽之誓”“荆棘玫瑰”和流明属性伤害主词条。2026-07-29 正式服上线时复核未发现相较打包数据的数值变化；数值来源和素材出处记录在 `web/assets/ASSET_SOURCES.md`。

蕾米埃尔已使用同一 `Agent Avatars` 素材体系于 2026-07-29 新增的 200×200 Q 版头像，来源与校验值记录在 `web/assets/ASSET_SOURCES.md`。

## V1.05B 扫描器使用

1. 解压完整 ZIP，保持主程序和 `scanner` 文件夹在同一目录。
2. 打开配装器，在“1. 录入驱动盘”顶部点击绿色“打开驱动盘扫描器”。
3. 在扫描器中依次点击“检测窗口”和“开始扫描”。
4. 扫描完成后打开产物文件夹。
5. 回到配装器，点击“导入 JSON（自动识别）”，选择 `export.json` 或 `export.partial.json`。

扫描器仅支持 Windows x64 和简体中文游戏界面。离线包自带 .NET 8、PP-OCRv5 ONNX 模型和运行库，不需要联网下载 OCR；识别效果仍可能受到 HDR、过饱和、分辨率、UI 缩放和窗口遮挡影响。

扫描器项目：<https://github.com/ZztIsolation/ZZZ-Scanner.Next>

## 数据互通

- “导出通用驱动盘 JSON”：交给 `ZztIsolation/zzz_calculator`。
- “导出完整备份”：保存本软件的库存、方案和本地占用关系。
- 扫描器和计算器文件默认与现有库存合并。
- `setId`、`maxLevel`、`source`、`raw`、游戏装备字段及未来扩展字段会保留并在再次导出时恢复。
- 卡片右上头像只表示 `discClaims[].character` 的本地方案占用，不表示游戏内实际装备关系。

## 从源码构建

```powershell
go test ./...
go vet ./...

# V1.05A：不显示扫描器入口
go build -buildvcs=false -trimpath -ldflags "-s -w -X main.releaseEdition=A" -o ZZZ_Drive_Optimizer_V1.05A.exe .

# V1.05B：显示扫描器入口，需要在 EXE 同级放置 scanner 文件夹
go build -buildvcs=false -trimpath -ldflags "-s -w -X main.releaseEdition=B" -o ZZZ_Drive_Optimizer_V1.05B.exe .
```

详细的长期发布规则见 [RELEASE_POLICY.md](RELEASE_POLICY.md)，历次错误与修正记录见 [CORRECTION_LOG.md](CORRECTION_LOG.md)，本版更新内容见 [RELEASE_NOTES_V1.05.md](RELEASE_NOTES_V1.05.md)。

## 素材说明

代理人和驱动盘图片全部离线嵌入；素材来源记录在 `web/assets/ASSET_SOURCES.md` 和 `web/assets/agents/Q_AVATAR_SOURCE_MANIFEST.json`。游戏相关素材版权归 HoYoverse 所有。
