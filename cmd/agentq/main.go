// Command agentq is the main binary for the agentq prototype.
// Use "agentq run --agent=<name>" to start an agent worker.
package main

import "github.com/shiblon/agentq/cmd/agentq/cmd"

func main() {
	cmd.Execute()
}
