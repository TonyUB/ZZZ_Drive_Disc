# ZZZ Drive Scan

`scan/` 是可独立复用的绝区零驱动盘导入核心，也是 ZZZ Drive Optimizer 的扫描数据入口。它按照一次完整的 `GetEquipDataScRsp` 构造稳定 JSON；同一个 `EquipInfo.uid` 始终映射为同一个 `zzz:<uid>`，因此重复扫描只会更新，不会制造副本。

## 安全边界

- 只支持用户主动授权的本地会话，不注入、不 Hook、不读取游戏内存、不绕过反作弊。
- 不保存账号 token、Cookie、二维码票据、RSA 明文或会话密钥；这些内容在导出 schema 中没有字段。
- 仅抓取驱动盘响应。未知客户端版本、未知模板、未知词条或非零 `retcode` 都会整批失败，避免生成看似成功的残缺库存。
- 被动监听官方客户端流量不能完成解密：登录阶段的客户端随机数不在明文网络流量中。版本适配器必须由主动授权会话提供者使用。

## 目录职责

- `schema.go`：稳定的 `scan-result.json` schema 与严格校验。
- `adapter.go`：每个区服/客户端版本的命令号、protobuf 字段号和 XOR 常量。
- `wire.go`：不依赖游戏生成代码的最小 protobuf 解码器。
- `catalog.go`：将模板 ID、属性 ID 转成套装、位置及配装器词条。
- `capture.go`：供区服登录/传输实现接入的 `Source` 接口。
- `session.go`：主动会话使用的 MT19937-64 会话流、XOR 与敏感字节清理工具。
- `cmd/zzz-drive-scan`：开发者解码、转换和适配器检查 CLI。

`examples/` 只是无真实协议信息的格式示例，明确不能用于游戏。真实适配器必须精确列出客户端版本并与同版本目录一起发布。

## 开发者用法

检查适配器：

```powershell
go run ./scan/cmd/zzz-drive-scan check-adapter `
  --adapter adapter.json `
  --client-version 3.0.0
```

将已经解密的 `GetEquipDataScRsp` protobuf body 转为配装器可导入文件：

```powershell
go run ./scan/cmd/zzz-drive-scan decode `
  --adapter adapter.json `
  --catalog catalog.json `
  --client-version 3.0.0 `
  --body equip-response.bin `
  --output scan-result.json
```

协议开发时也可以用已解码 JSON 固件验证目录映射：

```powershell
go run ./scan/cmd/zzz-drive-scan convert `
  --adapter adapter.json `
  --catalog catalog.json `
  --client-version 3.0.0 `
  --response response.json `
  --output scan-result.json
```

## 实现区服会话

实现 `scan.Source`，在 `FetchAll(context.Context)` 内完成一次性二维码授权、会话密钥协商和 `GetEquipDataCsReq` 请求，返回完整 `EquipmentResponse`。凭据只保留在该函数的最短生命周期内，并在返回前清除。登录与游戏协议会随区服和版本变化，不能在没有实机固件验证时复用旧命令号。

## 配装器导入保证

配装器先调用 `/api/scan/import/preview`，展示新增、更新、不变数量和一次性确认哈希；只有用户确认后才调用 `/api/scan/import/apply`。写入按稳定 ID upsert，保留配装器中的备注、废弃标记、归属和创建时间。
