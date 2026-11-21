# shotline

离屏活动卡片时间线核对工具：按时间线逐帧合成动态图层，并把每一帧降维成
可跨机器复算的 SHA-256 像素摘要。**不输出任何 PNG、视频或音频**，只落盘
JSONL 逐帧记录与汇总。

## 安装（macOS / Python 3.11）

```sh
brew install python@3.11
python3.11 -m venv .venv
.venv/bin/python -m pip install --upgrade pip
.venv/bin/python -m pip install -e .
```

## 运行测试与演示

```sh
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -m shotline demo \
  --width 320 --height 180 --fps 24 --duration 2 \
  --seed 17 --workers 4 --output /tmp/shotline-frames.jsonl
```

真实时间线文件（图层图片须位于时间线 JSON 同目录内）：

```sh
.venv/bin/python -m shotline run --timeline path/to/timeline.json \
  --workers 4 --output path/to/frames.jsonl
```

## 输出格式

每帧一行，末尾一行汇总：

```json
{"type":"frame","index":0,"pts_us":0,"pixel_sha256":"<64 hex>"}
{"type":"summary","frame_count":48,"fps":"24/1","cue_count":2,
 "max_abs_av_error_ms":0.0,"mean_abs_av_error_ms":0.0}
```

- `pts_us`：以有理数帧率精确计算的呈现时间戳（微秒），无浮点累积误差。
- `pixel_sha256`：对该帧连续 RGBA 字节计算的 SHA-256。
- 音频提示点按 `sample_index / sample_rate` 对齐到最近画面帧，汇总给出
  A/V 绝对误差的最大值与平均值（毫秒）。

## 架构与数据流

```
CLI (cli.py / __main__.py)
  -> model.py        时间线 JSON 校验：缺字段/重复时间点/非法数值/提示点越界
  -> timebase.py     有理数帧时钟（帧数向上取整）、提示点对齐
  -> assets.py       确定性演示素材生成 + 沙箱内本地 PNG 读取
  -> compositor.py   NumPy 关键帧插值、变换与四种自实现混合模式
  -> exporter.py     1..8 线程逐帧渲染、SHA-256、原子写 JSONL、失败清理
```

合成规则：图层按 `(z, 原始顺序)` 稳定排序；支持位置、缩放、裁切、关键帧
线性插值与负坐标；透明度与像素 alpha 共同参与 source-over 合成；混合模式
`normal / multiply / screen / add` 全部在 `compositor.py` 内以 NumPy 实现，
不依赖 Pillow 的合成接口。

## 退出码

| 码 | 含义 |
|----|------|
| 0  | 成功 |
| 2  | 参数或时间线无效 |
| 3  | 素材读取、合成或导出失败（临时文件会被清理） |

## 约束

- 画布边长 1..4096；本地图片路径经 `resolve()` 后必须留在时间线目录内。
- 样例素材与测试夹具全部由代码确定性生成，无外部服务或外部文件依赖。
- 多线程仅用于渲染，输出严格按帧索引归集，任何 worker 数结果逐字节一致。
