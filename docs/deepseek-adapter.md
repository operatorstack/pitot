# DeepSeek Harness

Pitot supports the official DeepSeek Harness (`dsh`) through its native shell
pre-execute middleware. The supported release is `@deepseek-ai/dsh@0.2.0-rc.2`,
a developer preview. It requires Node.js 22.19 or newer in the 22 series, or
Node.js 24 or newer.

Install that release and initialize the integration in your project:

```sh
npm install --global @deepseek-ai/dsh@0.2.0-rc.2
pitot init --host deepseek
pitot doctor --host deepseek
```

Start a prompt with your Pitot configuration:

```sh
pitot dev --host deepseek --prompt "Inspect this project" -- dsh
```

Configure a shell Controller in `pitot.json`. Shell execution requires an
explicit, matching Controller reply. A missing runtime, unavailable Controller,
timeout, malformed reply, or mismatched identity blocks execution, including
when the runtime's ordinary fallback policy permits an action. An explicit
Controller denial becomes a native tool error with code `PITOT_DENIED`.

Pitot creates a separate Harness profile and plugin configuration for each run.
It waits for that plugin to register before submitting the prompt. Existing
Harness settings are not used as integration configuration. DeepSeek's own
sandbox and other native middleware still apply after Pitot grants execution.
The default provider uses the Harness's official DeepSeek configuration and
credentials supplied through the process environment.

For an existing Pitot runtime, the single-prompt interface is:

```sh
pitot acp deepseek --runtime /absolute/path/runtime.json \
  --cwd /absolute/path/project --prompt "Inspect this project"
```

The integration mediates the native `bash` and `pwsh` tools. Other tools continue
through the Harness middleware unchanged. Cancellation asks the Harness to
dispose its native subprocesses on Unix; Windows uses a process Job. Unix
force termination cannot guarantee cleanup of subprocesses that escape the
Harness's native ownership range.
