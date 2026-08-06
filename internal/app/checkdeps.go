package app

import "sermo/internal/checks"

func checkDepsFromAppDeps(deps Deps, base checks.Deps) checks.Deps {
	base.Samplers = deps.Samplers
	return base
}
