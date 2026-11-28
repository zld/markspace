# releasepipe — 本地媒体发布内核

面向游戏版本发布的多轨时间线处理内核：读取 MP4 容器与 RTP 包流，按内容摘要
做增量重算，支持中断续跑与版本回滚。纯终端形态，无外部服务依赖。

## 构建与测试

```sh
go mod tidy
go build ./...
go test ./...
```

## 架构

| 模块 | 职责 |
| --- | --- |
| `internal/clock` | 有理数时间基准（公共纳秒基准）、RTP 序号/时间戳模数展开、漂移估计 |
| `internal/errs` | 统一错误模型：阶段 + 资产 + 稳定错误码 + 偏移 |
| `internal/ingest` | MP4 容器读取（保留原始 DTS/PTS、时基、逐样本 VFR 时长、来源位置）、RTP 乱序/丢包恢复、时间线归一与音画对齐 |
| `internal/fixture` | 确定性样例生成（MP4/RTP，全部代码合成，无外部文件） |
| `internal/index` | 内容摘要（源字节 + 轨道参数 + 封装元数据）、原子索引提交、分块检查点 |
| `internal/release` | 平台资产计划、按块渲染与复用、版本提交与回滚 |
| `cmd/releasepipe` | 验收入口（demo 子命令） |

数据流：MP4/RTP 输入 → 时间线归一（漂移修正、冲突检测）→ 摘要索引 →
平台资产计划 → 包清单 + 指标 + 版本记录。

## 验收流程

```sh
# 1. 正常发布：输出 sync_ms / recovered_pct / p95_ms / reused/rebuilt_assets / manifest_sha256
go run ./cmd/releasepipe demo --case good --platform ios,android --index .tmp/index

# 2. 截断样本：退出码 1，输出错误码与偏移，索引提交数不增加
go run ./cmd/releasepipe demo --case damaged --index .tmp/bad

# 3. 中断续跑：先在第 2 块后中断，再从第 3 块续跑；清单摘要与无中断运行一致
go run ./cmd/releasepipe demo --case interrupted --stop-after-block 2 --index .tmp/resume
go run ./cmd/releasepipe demo --case interrupted --stop-after-block 2 --index .tmp/resume --resume

# 4. 幂等重跑（产生第二个完整版本，清单摘要不变）
go run ./cmd/releasepipe demo --case interrupted --index .tmp/resume --resume

# 5. 回滚到最近一个完整提交，输出版本号与清单摘要
go run ./cmd/releasepipe demo --case good --index .tmp/resume --rollback
```

退出码：0 = 完成，1 = 输入或处理失败，2 = 参数错误。
注意：`go run` 会把非零退出码统一报告为 1（ stderr 中可见 `exit status 2` ）；
如需精确退出码，请 `go build -o releasepipe ./cmd/releasepipe` 后直接运行二进制。

## 设计要点

- **VFR**：逐样本时长来自 stts/RTP 时间戳差值，绝不用平均帧率补齐。
- **回绕与乱序**：RTP 序号（16 位）与时间戳（32 位）按模数展开；半模跳变
  无法确定先后关系时轨道失败（`AMBIGUOUS_ORDER`），不写入可发布状态。
- **摘要失效**：源字节、轨道参数、封装元数据任一变化都使对应资产失效；
  未变化资产复用，输出重算/复用清单。
- **检查点**：固定块（2 资产/块）先落数据块、再提交检查点；续跑时校验
  每块摘要，从最后完整块继续；重复运行不产生重复资产或不同清单。
- **回滚**：只切换到最近一个完整提交；损坏（摘要不符）或不完整版本
  不会成为回滚目标。
- **资源约束**：限制输入尺寸（容器 ≤1GiB、样本/包数上限），所有偏移、
  长度与整数运算均做溢出校验，损坏输入不会造成越界读取。
- 性能指标（p95_ms）按生成夹具计时，不外推为生产吞吐承诺。
