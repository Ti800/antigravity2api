# antigravity2api

把你自己的 Antigravity 订阅转成本地 OpenAI / Claude 兼容 API。为在 iOS 的 iSH 中运行、供 Minis 调用而设计。

## 变更说明

本仓库最初是 Gemini 网页版的代理：靠浏览器 cookie 认证、跟踪构建号、抓取每个模型的 ticket、模拟浏览器 TLS 指纹。这些对 Antigravity 都不适用——Antigravity 使用 Google OAuth 认证、向 Cloud Code 端点发送普通 JSON，因此网页版那套机制已全部移除。

继承下来的是在真机上实测过、而非拍脑袋的部分：

- 单静态二进制，无运行时依赖
- 冷启动不发起任何网络请求
- 服务脚本能识别并跳过 iOS 回收 App 后留下的"僵尸端口"
- 不设进程内守护：进程内的任何东西都不可能比 App 活得久

## 结构

分层参照 OpenCodex、方向相反。OpenCodex 把编码客户端路由到任意供应商；本项目把任意客户端接进同一份订阅。

| 包 | 职责 |
|---|---|
| `internal/translate` | OpenAI / Claude 请求与 Cloud Code 信封之间的转换 |
| `internal/upstream` | HTTP 传输、SSE 读取、项目发现 |
| `internal/auth` | 账号文件与 access token 刷新 |
| `internal/server` | 路由、本地鉴权、账号选择 |

入站协议与上游协议互相不 import：新增一种客户端格式 = 加一个 translator 和一个 handler，仅此而已。

## 构建

```sh
go build -trimpath -ldflags="-s -w" -o antigravity2api .
```

唯一依赖是 `github.com/google/uuid`。在 iSH 上，bootstrap Go 在切换到下载的工具链时会段错误，直接调用工具链二进制：

```sh
TC=$(ls -d /root/go/pkg/mod/golang.org/toolchain@*/ | head -1)
"$TC/bin/go" build -trimpath -ldflags="-s -w" -o antigravity2api .
```

二进制约 6.3 MB；空闲堆约 0.5 MB，进程总计约 12 MB。

## 配置

Token 刷新需要 OAuth client id 与 secret，从环境变量读取（不编译进二进制）：

```sh
export ANTIGRAVITY_CLIENT_ID=...
export ANTIGRAVITY_CLIENT_SECRET=...
```

`scripts/service.sh` 会加载同目录下的 `.env`（导出后再启动二进制），该文件已被 gitignore。

IDE user agent 固定写在二进制里（`antigravity/ide/2.5.5 (...)`）。Google 偶尔会把新模型限制在更高版本的 IDE 之后，可用 `ANTIGRAVITY_USER_AGENT` 覆盖，无需重新编译。

每个账号是 `auth/` 下的一个 JSON 文件：

```json
{
  "email": "you@example.com",
  "refresh_token": "...",
  "project_id": ""
}
```

`project_id` 可留空：首次请求时会自动发现——前提是上游请求携带 IDE user agent（`antigravity/ide/<version>`）。通用 UA 可以列模型，但会被项目发现环节拒绝，表现看起来像"账号没有项目"，其实不是。

然后：

```sh
cp config.example.json config.json
sh scripts/service.sh start
```

Minis 里添加 OpenAI 兼容 provider，Base URL 填 `http://127.0.0.1:8081`，开启 appendV1Suffix。对话要绑定"带 fallback 的模型组"，不要绑定单个本地模型——服务掉线时单模型绑定会硬失败。

`/var/minis` 会丢可执行位；若报 `Permission denied`，先把二进制拷到 `/tmp`、`chmod +x`、再拷回来。

## 模型

`GET /v1/models` 返回上游当前公布的模型 id（来自 `fetchAvailableModels`，缓存十分钟；拉取失败时使用一小份静态兜底列表）。请按 id 原样请求——当前 flash 世代按 wire id 分档（`gemini-3.8-flash-low` / `-medium` / `-high`），Pro 档是 `gemini-pro-agent`，`claude-*` / `gpt-oss-*` 则直通对应模型。

几个旧写法会自动解析，兼容老配置：

| 请求名 | 解析为 |
|---|---|
| `gemini-3.8-flash` | `gemini-3.8-flash-medium` |
| `gemini-3.8-flash-thinking` | `gemini-3.8-flash-high` |
| `gemini-3.7-flash` / `gemini-3.6-flash` | 该世代的 `-medium` 档 |
| `gemini-3.1-pro` | `gemini-pro-agent` |

重放的 tool call 必须携带 thought signature：没有捕获到真实签名时，请求会在重放的模型回合中、给第一个 functionCall 填上官方哨兵值 `skip_thought_signature_validator`（仅限 Gemini 系模型）。

## 端点

- `GET /healthz` —— 含 pid，便于服务脚本区分"自己的进程"和端口上的其他东西
- `GET /v1/models`
- `POST /v1/chat/completions` —— 流式、工具调用、reasoning 内容
- `POST /v1/messages` —— Claude 形态入参，走同一条链路

## iOS 上的限制

杀掉 Minis App 会连带整个环境：cron、nohup、任何 goroutine 都随之消失，除了重新 `sh scripts/service.sh start` 没有别的恢复手段。低电量模式会在约两分钟内杀掉后台进程——需要服务跨 App 切换存活时请关掉它。

### 宿主环境会周期性回收 Go 进程（2026-09-22 实测）

参考设备上，iOS 的 shell 环境大约**每 180 秒**向 **Go** 进程发送 SIGKILL——已跨 Go 1.23/1.26
工具链、不同二进制位置与运行参数验证；python/shell 进程不受影响。`scripts/keepalive.sh`
是一个 shell 侧看护（shell 循环不受回收影响），能在数秒内把服务拉回来：

```sh
nohup sh scripts/keepalive.sh >/dev/null 2>&1 &
```

## License

MIT
