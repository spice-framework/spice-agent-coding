// Package tuittest provides a deterministic, agent-friendly harness for the
// Spice Agent TUI.
//
// It is designed so coding agents and humans can:
//
//   - drive semantic keyboard interaction without a real PTY;
//   - inject Session updates without a daemon;
//   - capture exact terminal frames (styled and plain);
//   - interpret real VT output, cursor state, alternate-screen mode, and resize;
//   - assert pixel-perfect goldens with optional UPDATE_GOLDEN refresh;
//   - dump multi-format screen reports that agents can read in logs.
//
// The harness uses the real presentation Model and FixedRenderer. Its virtual
// terminal interprets output only; it does not start a child process or claim a
// native PTY/ConPTY boundary. The package never discovers a daemon or performs
// network I/O.
//
// Typical agent workflow:
//
//	driver, err := tuittest.NewDriver(tuittest.Options{Width: 48, Height: 12})
//	// ...
//	defer driver.Close()
//	_ = driver.Type("list owners")
//	_ = driver.Key("enter")
//	screen, _ := driver.Screen("list-owners")
//	fmt.Print(screen.AgentReport())
//	screen.AssertGolden(t, "testdata", "list-owners")
package tuittest
