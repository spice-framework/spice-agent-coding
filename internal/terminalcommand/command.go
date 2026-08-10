package terminalcommand

import (
	"context"
	"io"
)

type Command struct{}

func (Command) Execute(
	ctx context.Context,
	arguments []string,
	stdout io.Writer,
	stderr io.Writer,
	runner Runner,
) int {
	command := Command{}
	stdout = command.nonNilWriter(stdout)
	stderr = command.nonNilWriter(stderr)
	options, help, valid := (Parser{}).Parse(arguments)
	if help {
		if !command.write(stdout, usage) {
			return ExitFailure
		}
		return ExitSuccess
	}
	if !valid {
		if !command.write(stderr, invalidArguments) || !command.write(stderr, usage) {
			return ExitFailure
		}
		return ExitUsage
	}
	if ctx == nil || runner == nil || ctx.Err() != nil {
		if !command.write(stderr, runtimeFailure) {
			return ExitFailure
		}
		return ExitFailure
	}
	if options.Mode() != ModeCheck && !command.write(stderr, capabilityWarning) {
		return ExitFailure
	}
	if command.runnerFailed(ctx, runner, options) {
		if !command.write(stderr, runtimeFailure) {
			return ExitFailure
		}
		return ExitFailure
	}
	return ExitSuccess
}

func (Command) runnerFailed(
	ctx context.Context,
	runner Runner,
	options Options,
) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	return runner.Run(ctx, options) != nil
}

func (Command) nonNilWriter(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
}

func (Command) write(writer io.Writer, value string) bool {
	written, err := io.WriteString(writer, value)
	return err == nil && written == len(value)
}
