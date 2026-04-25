package agent

import "actrail/internal/adapters/process"

func (c Catalog) LaunchSpec(req Request) (process.LaunchSpec, error) {
	if err := c.ValidateRequest(req); err != nil {
		return process.LaunchSpec{}, err
	}
	adapter, err := c.Adapter(req.Backend())
	if err != nil {
		return process.LaunchSpec{}, err
	}
	args, err := adapter.CommandArgs(req.Options())
	if err != nil {
		return process.LaunchSpec{}, err
	}
	command, err := process.NewCommand(req.BinPath().String(), args...)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return process.NewLaunchSpec(command, req.CWD().String(), req.Environment(), req.IO())
}

func (r Request) LaunchSpec() (process.LaunchSpec, error) {
	return DefaultCatalog().LaunchSpec(r)
}
