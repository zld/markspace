# mediakey

本地媒体加密与校验工具：在资源进入构建包前，对过场视频和分轨音频的分片做
AES-256-GCM 加密，并基于本地签名许可证逐片校验。纯本地运行，仅使用 Go 1.22
标准库，无第三方依赖、无网络访问；根密钥只驻留内存，测试密钥由固定种子派生，
**禁止作为生产密钥使用**。

## 构建与验证

```sh
make build          # 产出 bin/mediakey
go test ./...       # 全量单元与跨模块测试
make test-race      # 带竞态检测的测试
make demo-check     # 演示 + JSON 字段/数量/故障类别断言
make verify-demo    # encrypt/verify 文件端到端冒烟
```

## 子命令

| 命令 | 作用 |
| --- | --- |
| `demo` | 生成确定性样例内容并演示完整加密/校验链路与故障注入 |
| `encrypt` | 把本地文件按固定字节数分片加密，输出 JSON 清单 |
| `verify` | 校验清单，输出每片一条的稳定 JSON 报告 |

退出码契约（可用于脚本）：

- `0` 运行完成（允许部分分片校验失败）
- `2` 参数错误（缺路径、未知策略/注入名、非法轮换间隔）
- `3` 清单或输入格式错误
- `4` 许可证或密钥分发初始化失败

标准演示：

```sh
go run ./cmd/mediakey demo -chunks 12 -chunk-size 4096 \
  -rotate-every 3 -inject expired,replay,tamper
```

`-inject` 接受逗号分隔的 `expired`、`replay`、`tamper`、`out-of-window`，
名称会被规范化（去空格、转小写），未知名称以退出码 2 拒绝。

## 报告结构

报告带 `report_version` 字段，`chunk_results` 按 `chunk_index` 稳定排序，
每个分片始终出现，即使其他分片失败：

```json
{
  "report_version": "1.0",
  "asset_id": "demo-asset-0001",
  "rotation_count": 3,
  "chunk_results": [
    {"chunk_index": 5, "key_id": "key-r001", "status": "failed",
     "failure_reason": "replay", "detail": "replay: nonce already seen"}
  ],
  "summary": {"total": 12, "ok": 9, "failed": 3,
              "reasons": {"auth_failed": 1, "expired": 1, "replay": 1}}
}
```

失败只报告稳定错误类别（`failure_reason`），任何输出都不含密钥或明文。

## 模块与数据流

```
fixture（确定性样例/故障）
   │
   ▼
manifest（素材、分片、信封、限额校验）
   │
   ▼
license（HMAC 签名、范围与有效期，含轮换重叠 grace 窗口）
   │
   ▼
keys（根密钥派生轮次密钥，按分片序号轮换、撤销、关闭）
   │
   ▼
crypto（AES-256-GCM；AAD 绑定素材 ID + 分片序号 + 密钥 ID）
   │
   ▼
replay（nonce 去重、序号单调、容量/时间窗口、淘汰墓碑不放行）
   │
   ▼
pipeline（并发编排，单片失败不掩盖后续分片）→ report（稳定 JSON）
```

## 关键语义

- **轮换计数**：只统计成功激活的新轮次；重复激活是幂等 no-op。
- **过期与重叠**：许可证过期后新解密被拒，报告仍记录分片标注的 `key_id`；
  `grace-window` 内的旧轮次允许完成校验，窗口外标记 `key_expired`。
- **重放防护**：重复 nonce → `replay`；序号回退 → `sequence_out_of_order`；
  被容量淘汰但仍在窗口内 → `window_outside`，淘汰不会默认放行。
- **并发**：同一 nonce 的竞争请求只有一个成功（见
  `internal/pipeline/concurrent_test.go`），轮换计数与分发状态在
  `go test -race` 下保持一致。
- **安全限额**：素材大小、分片数、分片大小、缓存容量与轮次数都有上限，
  超限直接拒绝，防止整数溢出和不受控内存分配。
