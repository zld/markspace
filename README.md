# ASS Subtitle Pipeline

离线 ASS 字幕排版管线：把叙事团队交付的 ASS 文件解析为可重复渲染的关卡对白资产。
纯本地运行、无窗口、无外部服务依赖；样式继承与行内覆盖标签被完整消解后交给
libass 离屏渲染，输出每条字幕的画面区域（坐标/宽高）、PNG 图片与 SHA-256 摘要。

## 技术栈

- C++17、CMake ≥ 3.28
- libass（0.17.3 起 API 兼容；在 0.17.5 上开发验证）
- zlib（PNG 编码）、自研 SHA-256（无外部加密库）
- 仅终端界面，无 GUI 依赖

## 安装与构建

```sh
brew install cmake pkg-config libass
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
```

### Apple Silicon 与 Intel 双架构

默认按本机架构原生构建（Apple Silicon 上为 arm64，Intel Mac 上为 x86_64）。
代码为架构中立的标准 C++17。若需要单文件通用二进制，且所有 Homebrew 依赖
（libass 及其传递依赖）均提供两个切片，可开启：

```sh
cmake -S . -B build-u -DCMAKE_BUILD_TYPE=Release -DBUILD_UNIVERSAL=ON
cmake --build build-u --parallel
lipo -archs build-u/ass_pipeline   # arm64 x86_64
```

注意：标准 Homebrew 安装只含本机架构切片；跨架构链接需要对应的通用依赖。

## 运行

生成内置确定性样例并渲染：

```sh
./build/ass_pipeline demo --canvas 1280x720 --output out --json out/summary.json
```

渲染外部 ASS 文件：

```sh
./build/ass_pipeline render --input path/to/sub.ass \
    --canvas 1280x720 --output out --json out/summary.json
```

参数：

| 参数 | 说明 |
| --- | --- |
| `demo` / `render` | 内置样例 / 外部文件 |
| `--canvas WxH` | 固定画布尺寸，如 `1280x720`、`1920x1080` |
| `--output DIR` | PNG 输出目录（自动创建） |
| `--json FILE` | 摘要清单路径 |
| `--input FILE` | `render` 模式的输入 ASS |
| `--fonts-dir DIR` | 额外字体目录（可选） |

终端输出字段：`styles`、`events`、`inheritance_errors`、`inline_tags`、
`unknown_tags`、`rendered`；每条字幕记录含 `event_id`、`x`、`y`、`width`、
`height`、`sha256`（64 位十六进制）。

## 测试与一键验收

```sh
ctest --test-dir build --output-on-failure
# 或完整流程（干净构建 + 演示渲染 + 清单校验 + 测试）
./scripts/verify.sh
```

## 退出码

| 码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 2 | 无效输入（时间戳/字段错误、超 32 MiB、超 10000 事件、路径冲突等） |
| 3 | 渲染失败（libass 无可见字形、PNG 编码/落盘失败等） |

继承缺陷（缺失父样式、自继承、环路）会让相关样式标记为无效且不进入渲染，
其余样式仍正常渲染；诊断带文件行号与事件编号。

## 模块结构

```
include/            数据模型与各模块接口
src/style_parser.cpp    [V4+ Styles]/[Events] 解析、时间戳校验
src/inheritance.cpp     继承图、祖先链、环路/缺失/自继承诊断、字段回溯消解
src/tag_resolver.cpp    行内覆盖标签按序消解、未闭合块保守恢复
src/layout_engine.cpp   libass 生命周期、单事件脚本合成、字形混合
src/layout_event.cpp    逐事件排版、覆盖标签应用、包围盒
src/sha256.cpp / src/png_writer.cpp   摘要与离屏 PNG
src/fixture_builder.cpp 确定性样例生成
src/pipeline.cpp / src/main.cpp       编排与命令行
tests/              集成测试（解析/继承/标签/排版/边界/退出码）
```

## 约束与安全

- 输入上限 32 MiB、10000 事件；畸形输入不崩溃（有专门鲁棒性测试）。
- 所有输出仅写入指定目录；拒绝把输出/清单写到输入文件路径。
- 字体缺失给出诊断，不访问网络下载字体（macOS CoreText 可能回退到替代字体）。
- 样例由代码生成，不引用任何外部文件；两次运行的 SHA-256 与几何完全一致。
