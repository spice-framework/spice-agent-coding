# Configuration

The generated daemon reads configuration from the process environment. Spice
validates required values before serving and marks the API key as a secret so
it cannot appear in generated metadata or diagnostics.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `OPENAI_API_KEY` | yes | — | OpenAI credential; always secret-redacted |
| `OPENAI_MODEL` | yes | — | Responses API model |
| `OPENAI_BASE_URL` | no | `https://api.openai.com/v1` | Responses API base URL |
| `OPENAI_ORGANIZATION` | no | empty | OpenAI organization |
| `OPENAI_PROJECT` | no | empty | OpenAI project |
| `OPENAI_TIMEOUT` | no | `2m` | Provider operation timeout |
| `OPENAI_MAX_RETRIES` | no | `0` | Pre-stream retries only |
| `SPICE_AGENT_WORKSPACE` | no | `.` | Coding-tool workspace |
| `SPICE_AGENT_RUN_AUTHORITY_DIRECTORY` | no | platform default | Private run-authority state |

One optional runtime-tool plugin may be selected explicitly. Setting any
non-default plugin field opts into the plugin contract; a required plugin must
have a complete valid configuration.

| Variable | Default |
| --- | --- |
| `SPICE_AGENT_RUNTIME_PLUGIN_REQUIRED` | `false` |
| `SPICE_AGENT_RUNTIME_PLUGIN_ID` | `runtime-tool` |
| `SPICE_AGENT_RUNTIME_PLUGIN_PATH` | empty |
| `SPICE_AGENT_RUNTIME_PLUGIN_SHA256` | empty |
| `SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_NAME` | empty |
| `SPICE_AGENT_RUNTIME_PLUGIN_MANIFEST_VERSION` | empty |
| `SPICE_AGENT_RUNTIME_PLUGIN_WORKING_DIRECTORY` | empty |
| `SPICE_AGENT_RUNTIME_PLUGIN_STARTUP_TIMEOUT` | `10s` |
| `SPICE_AGENT_RUNTIME_PLUGIN_CALL_TIMEOUT` | `2m` |
| `SPICE_AGENT_RUNTIME_PLUGIN_DRAIN_TIMEOUT` | `10s` |
| `SPICE_AGENT_RUNTIME_PLUGIN_SHUTDOWN_TIMEOUT` | `10s` |
| `SPICE_AGENT_RUNTIME_PLUGIN_CONTAINMENT_TIMEOUT` | `5s` |

Capability declarations use the same prefix with
`FILESYSTEM_READ`, `FILESYSTEM_WRITE`, `PROCESS_EXECUTE`, `NETWORK_ACCESS`,
`SECRETS_READ`, `ENVIRONMENT_READ`, and `ENVIRONMENT_WRITE`. They default to
`false` and document trusted authority; they are not a sandbox.

Terminal managed, attach, and check modes are injected from validated command
arguments. They are not mutable global configuration.
