package daemonprocess

func newTestStarter(config Config, executable, currentExecutable string) (*Starter, error) {
	starter := &Starter{}
	if err := starter.configure(config, executable, currentExecutable); err != nil {
		return nil, err
	}
	return starter, nil
}
