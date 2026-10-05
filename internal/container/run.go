package container

import "fmt"

// Run executes a command in the given exec environment and waits for it to
// finish. It is a synchronous convenience over (env.Exec, env.Wait): a
// non-zero exit code becomes an error, transport errors are wrapped with the
// argv[0] for context.
//
// req.Env nil/empty means inherit the stub's environment. req.FDs nil/empty
// means inherit the stub's stdio. Callers needing /dev/null or pipes set
// req.FDs explicitly.
func Run(env ExecEnv, req *ExecRequest) error {
	pid, err := env.Exec(req)
	if err != nil {
		return fmt.Errorf("exec %s: %w", req.Argv[0], err)
	}
	code, err := env.Wait(pid)
	if err != nil {
		return fmt.Errorf("wait %s: %w", req.Argv[0], err)
	}
	if code != 0 {
		return fmt.Errorf("%s exited with code %d", req.Argv[0], code)
	}
	return nil
}
