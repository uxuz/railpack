package python

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/railwayapp/railpack/core/generate"
	"github.com/railwayapp/railpack/core/plan"
	"github.com/railwayapp/railpack/internal/utils"
)

const (
	DEFAULT_PYTHON_VERSION = "3.11"
	UV_CACHE_DIR           = "/opt/uv-cache"
	PIP_CACHE_DIR          = "/opt/pip-cache"
	VENV_PATH              = "/app/.venv"
	LOCAL_BIN_PATH         = "/root/.local/bin"
)

type PythonProvider struct{}

func (p *PythonProvider) Name() string {
	return "python"
}

func (p *PythonProvider) Initialize(ctx *generate.GenerateContext) error {
	return nil
}

func (p *PythonProvider) Detect(ctx *generate.GenerateContext) (bool, error) {
	hasPython := ctx.App.HasMatch("main.py") ||
		p.hasRequirements(ctx) ||
		p.hasPyproject(ctx) ||
		p.hasPipfile(ctx)

	return hasPython, nil
}

func (p *PythonProvider) Plan(ctx *generate.GenerateContext) error {
	p.InstallMisePackages(ctx, ctx.GetMiseStepBuilder())

	install := ctx.NewCommandStep("install")
	install.AddInput(plan.NewStepLayer(p.GetBuilderDeps(ctx).Name()))

	install.Secrets = []string{}
	install.UseSecretsWithPrefixes([]string{"PYTHON", "PIP", "PIPX", "UV", "PDM", "POETRY"})

	installOutputs := []string{}

	if p.hasRequirements(ctx) {
		installOutputs = p.InstallPip(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasUv(ctx) {
		installOutputs = p.InstallUv(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasPoetry(ctx) {
		installOutputs = p.InstallPoetry(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasPdm(ctx) {
		installOutputs = p.InstallPDM(ctx, install)
	} else if p.hasPipfile(ctx) {
		installOutputs = p.InstallPipenv(ctx, install)
	}

	p.addMetadata(ctx)

	build := ctx.NewCommandStep("build")
	build.AddInput(plan.NewStepLayer(install.Name()))
	build.AddCommand(plan.NewCopyCommand("."))

	ctx.Deploy.StartCmd = p.GetStartCommand(ctx)
	maps.Copy(ctx.Deploy.Variables, p.GetPythonEnvVars(ctx))

	installArtifacts := plan.NewStepLayer(build.Name(), plan.Filter{
		Include: installOutputs,
	})

	p.AddRuntimeDeps(ctx)

	ctx.Deploy.AddInputs([]plan.Layer{
		ctx.GetMiseStepBuilder().GetLayer(),
		installArtifacts,
		plan.NewStepLayer(build.Name(), plan.Filter{
			Include: []string{"."},
			Exclude: []string{strings.TrimPrefix(VENV_PATH, "/app/")},
		}),
	})

	return nil
}

func (p *PythonProvider) GetStartCommand(ctx *generate.GenerateContext) string {
	startCommand := ""

	if p.isDjango(ctx) {
		startCommand = p.getDjangoStartCommand(ctx)
	}

	mainPythonFile := p.getMainPythonFile(ctx)
	hasMainPythonFile := mainPythonFile != ""

	if p.isFasthtml(ctx) && hasMainPythonFile && p.usesDep(ctx, "uvicorn") {
		startCommand = "uvicorn main:app --host 0.0.0.0 --port ${PORT:-8000}"
	}

	if p.isFlask(ctx) && hasMainPythonFile && p.usesDep(ctx, "gunicorn") {
		startCommand = "gunicorn --bind 0.0.0.0:${PORT:-8000} main:app"
	}

	if startCommand == "" && hasMainPythonFile {
		startCommand = fmt.Sprintf("python %s", mainPythonFile)
	}

	return startCommand
}

func (p *PythonProvider) getMainPythonFile(ctx *generate.GenerateContext) string {
	for _, file := range []string{"main.py", "app.py", "bot.py", "hello.py", "server.py"} {
		if ctx.App.HasMatch(file) {
			return file
		}
	}
	return ""
}

func (p *PythonProvider) StartCommandHelp() string {
	return "To start your Python application, Railpack will automatically:\n\n" +
		"1. Start FastAPI projects with uvicorn\n" +
		"2. Start Flask projects with gunicorn\n" +
		"3. Start Django projects with the gunicorn production server\n\n" +
		"Otherwise, it will run the main.py or app.py file in your project root"
}

func (p *PythonProvider) InstallUv(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using uv")

	install.AddCache(ctx.Caches.AddCache("uv", UV_CACHE_DIR))
	install.AddEnvVars(map[string]string{
		"UV_COMPILE_BYTECODE": "1",
		"UV_LINK_MODE":        "copy",
		"UV_CACHE_DIR":        UV_CACHE_DIR,
		"UV_PYTHON_DOWNLOADS": "never",
		"VIRTUAL_ENV":         VENV_PATH,
	})

	install.AddEnvVars(p.GetPythonEnvVars(ctx))

	p.copyInstallFiles(ctx, install)
	install.AddCommands([]plan.Command{
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewPathCommand(VENV_PATH + "/bin"),
		plan.NewExecCommand("uv sync --locked --no-dev --no-install-project"),
		plan.NewCopyCommand("."),
		plan.NewExecCommand("uv sync --locked --no-dev --no-editable"),
	})

	return []string{VENV_PATH}
}

func (p *PythonProvider) InstallPipenv(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pipenv")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"PIPENV_CHECK_UPDATE":       "false",
		"PIPENV_VENV_IN_PROJECT":    "1",
		"PIPENV_IGNORE_VIRTUALENVS": "1",
	})

	install.AddCommands([]plan.Command{
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	if ctx.App.HasMatch("Pipfile.lock") {
		install.AddCommands([]plan.Command{
			plan.NewCopyCommand("Pipfile"),
			plan.NewCopyCommand("Pipfile.lock"),
			plan.NewExecCommand("pipenv install --deploy --ignore-pipfile"),
		})
	} else {
		install.AddCommands([]plan.Command{
			plan.NewCopyCommand("Pipfile"),
			plan.NewExecCommand("pipenv install --skip-lock"),
		})
	}

	return []string{VENV_PATH}
}

func (p *PythonProvider) InstallPDM(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pdm")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"PDM_CHECK_UPDATE": "false",
	})

	p.copyInstallFiles(ctx, install)
	install.AddCommands([]plan.Command{
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewPathCommand(VENV_PATH + "/bin"),
		plan.NewExecCommand("pdm install --check --prod --no-editable"),
	})

	return []string{VENV_PATH}
}

func (p *PythonProvider) InstallPoetry(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using poetry")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"VIRTUAL_ENV":                   VENV_PATH,
		"POETRY_VIRTUALENVS_PATH":       VENV_PATH,
		"POETRY_VIRTUALENVS_IN_PROJECT": "true",
	})

	p.copyInstallFiles(ctx, install)
	install.AddCommands([]plan.Command{
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewPathCommand(VENV_PATH + "/bin"),
		plan.NewExecCommand("poetry install --no-interaction --no-ansi --only main --no-root"),
	})

	return []string{VENV_PATH}
}

func (p *PythonProvider) InstallPip(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pip")

	install.AddCache(ctx.Caches.AddCache("pip", PIP_CACHE_DIR))
	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"PIP_CACHE_DIR": PIP_CACHE_DIR,
		"VIRTUAL_ENV":   VENV_PATH,
	})

	install.AddCommands([]plan.Command{
		plan.NewExecCommand(fmt.Sprintf("python -m venv %s", VENV_PATH)),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})
	p.copyInstallFiles(ctx, install)
	install.AddCommands([]plan.Command{
		plan.NewExecCommand("pip install -r requirements.txt"),
	})

	return []string{VENV_PATH}
}

func (p *PythonProvider) AddRuntimeDeps(ctx *generate.GenerateContext) {
	for dep, requiredPkgs := range pythonRuntimeDepRequirements {
		if p.usesDep(ctx, dep) {
			ctx.Logger.LogInfo("Installing runtime apt packages for %s", dep)
			ctx.Deploy.AddAptPackages(requiredPkgs)
		}
	}

	if p.usesPostgres(ctx) {
		ctx.Deploy.AddAptPackages([]string{"libpq5"})
	}

	if p.usesMysql(ctx) {
		ctx.Deploy.AddAptPackages([]string{"default-mysql-client"})
	}
}

func (p *PythonProvider) GetBuilderDeps(ctx *generate.GenerateContext) *generate.MiseStepBuilder {
	miseStep := ctx.GetMiseStepBuilder()
	miseStep.SupportingAptPackages = append(miseStep.SupportingAptPackages, "python3-dev")

	for dep, requiredPkgs := range pythonBuildDepRequirements {
		if p.usesDep(ctx, dep) {
			ctx.Logger.LogInfo("Installing build apt packages for %s", dep)
			miseStep.SupportingAptPackages = append(miseStep.SupportingAptPackages, requiredPkgs...)
		}
	}

	if p.usesPostgres(ctx) {
		miseStep.SupportingAptPackages = append(miseStep.SupportingAptPackages, "libpq-dev")
	}

	if p.usesMysql(ctx) {
		miseStep.SupportingAptPackages = append(miseStep.SupportingAptPackages, "default-libmysqlclient-dev")
	}

	return miseStep
}

func (p *PythonProvider) InstallMisePackages(ctx *generate.GenerateContext, miseStep *generate.MiseStepBuilder) {
	python := miseStep.Default("python", DEFAULT_PYTHON_VERSION)

	if envVersion, varName := ctx.Env.GetConfigVariable("PYTHON_VERSION"); envVersion != "" {
		miseStep.Version(python, envVersion, varName)
	}

	if versionFile, err := ctx.App.ReadFile(".python-version"); err == nil {
		miseStep.Version(python, utils.ExtractSemverVersion(string(versionFile)), ".python-version")
	}

	if runtimeFile, err := ctx.App.ReadFile("runtime.txt"); err == nil {
		miseStep.Version(python, utils.ExtractSemverVersion(string(runtimeFile)), "runtime.txt")
	}

	if pipfileVersion, pipfileVarName := parseVersionFromPipfile(ctx); pipfileVersion != "" {
		miseStep.Version(python, pipfileVersion, fmt.Sprintf("Pipfile > %s", pipfileVarName))
	}

	if p.hasPoetry(ctx) || p.hasUv(ctx) || p.hasPdm(ctx) || p.hasPipfile(ctx) {
		miseStep.Default("pipx", "latest")
	}

	if p.hasPoetry(ctx) {
		miseStep.Default("pipx:poetry", "latest")
	}

	if p.hasPdm(ctx) {
		miseStep.Default("pipx:pdm", "latest")
	}

	if p.hasUv(ctx) {
		miseStep.Default("pipx:uv", "latest")
	}

	if p.hasPipfile(ctx) {
		miseStep.Default("pipx:pipenv", "latest")
	}

}

func (p *PythonProvider) GetPythonEnvVars(ctx *generate.GenerateContext) map[string]string {
	return map[string]string{
		"PYTHONFAULTHANDLER":            "1",
		"PYTHONUNBUFFERED":              "1",
		"PYTHONHASHSEED":                "random",
		"PYTHONDONTWRITEBYTECODE":       "1",
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",
		"PIP_DEFAULT_TIMEOUT":           "100",
	}
}

func (p *PythonProvider) copyInstallFiles(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) {
	if p.installNeedsAllFiles(ctx) {
		install.AddCommand(plan.NewCopyCommand("."))
		return
	}

	patterns := []string{
		"requirements.txt",
		"pyproject.toml",
		"Pipfile",
		"poetry.lock",
		"uv.lock",
		"pdm.lock",
	}

	for _, pattern := range patterns {
		if files, err := ctx.App.FindFiles(pattern); err == nil {
			for _, file := range files {
				install.AddCommand(plan.NewCopyCommand(file))
			}
		}
	}
}

func (p *PythonProvider) installNeedsAllFiles(ctx *generate.GenerateContext) bool {
	if requirementsContent, err := ctx.App.ReadFile("requirements.txt"); err == nil {
		return strings.Contains(requirementsContent, "file://")
	}

	if pyprojectContent, err := ctx.App.ReadFile("pyproject.toml"); err == nil {
		return strings.Contains(pyprojectContent, "file://") || strings.Contains(pyprojectContent, "path = ")
	}

	return false
}

func (p *PythonProvider) usesPostgres(ctx *generate.GenerateContext) bool {
	djangoPythonRe := regexp.MustCompile(`django.db.backends.postgresql`)
	containsDjangoPostgres := len(ctx.App.FindFilesWithContent("**/*.py", djangoPythonRe)) > 0
	return p.usesDep(ctx, "psycopg2") || p.usesDep(ctx, "psycopg2-binary") || containsDjangoPostgres
}

func (p *PythonProvider) usesMysql(ctx *generate.GenerateContext) bool {
	djangoPythonRe := regexp.MustCompile(`django.db.backends.mysql`)
	containsDjangoMysql := len(ctx.App.FindFilesWithContent("**/*.py", djangoPythonRe)) > 0
	return p.usesDep(ctx, "mysqlclient") || containsDjangoMysql
}

func (p *PythonProvider) addMetadata(ctx *generate.GenerateContext) {
	hasPoetry := p.hasPoetry(ctx)
	hasPdm := p.hasPdm(ctx)
	hasUv := p.hasUv(ctx)

	pkgManager := "pip"

	if hasPoetry {
		pkgManager = "poetry"
	} else if hasPdm {
		pkgManager = "pdm"
	} else if hasUv {
		pkgManager = "uv"
	}

	ctx.Metadata.Set("pythonPackageManager", pkgManager)
	ctx.Metadata.Set("pythonRuntime", p.getRuntime(ctx))
}

func (p *PythonProvider) usesDep(ctx *generate.GenerateContext, dep string) bool {
	for _, file := range []string{"requirements.txt", "pyproject.toml", "Pipfile"} {
		if contents, err := ctx.App.ReadFile(file); err == nil {
			// TODO: Do something better than string comparison
			if strings.Contains(strings.ToLower(contents), strings.ToLower(dep)) {
				return true
			}
		}
	}
	return false
}

var pipfileFullVersionRegex = regexp.MustCompile(`python_full_version\s*=\s*['"]([0-9.]*)"?`)
var pipfileShortVersionRegex = regexp.MustCompile(`python_version\s*=\s*['"]([0-9.]*)"?`)

func parseVersionFromPipfile(ctx *generate.GenerateContext) (string, string) {
	pipfile, err := ctx.App.ReadFile("Pipfile")
	if err != nil {
		return "", ""
	}

	if matches := pipfileFullVersionRegex.FindStringSubmatch(string(pipfile)); len(matches) > 1 {
		return matches[1], "python_full_version"
	}

	if matches := pipfileShortVersionRegex.FindStringSubmatch(string(pipfile)); len(matches) > 1 {
		return matches[1], "python_version"
	}

	return "", ""
}

func (p *PythonProvider) hasRequirements(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("requirements.txt")
}

func (p *PythonProvider) hasPyproject(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("pyproject.toml")
}

func (p *PythonProvider) hasPipfile(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("Pipfile")
}

func (p *PythonProvider) hasPoetry(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("poetry.lock")
}

func (p *PythonProvider) hasPdm(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("pdm.lock")
}

func (p *PythonProvider) hasUv(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("uv.lock")
}

func (p *PythonProvider) isFasthtml(ctx *generate.GenerateContext) bool {
	return p.usesDep(ctx, "python-fasthtml")
}

func (p *PythonProvider) isFlask(ctx *generate.GenerateContext) bool {
	return p.usesDep(ctx, "flask")
}

func (p *PythonProvider) getRuntime(ctx *generate.GenerateContext) string {
	if p.isDjango(ctx) {
		return "django"
	} else if p.isFlask(ctx) {
		return "flask"
	} else if p.usesDep(ctx, "fastapi") {
		return "fastapi"
	} else if p.isFasthtml(ctx) {
		return "fasthtml"
	}

	return "python"
}

// Mapping of python dependencies to required apt packages

var pythonBuildDepRequirements = map[string][]string{
	"pycairo": {"libcairo2-dev"},
}

var pythonRuntimeDepRequirements = map[string][]string{
	"pycairo":   {"libcairo2"},
	"pdf2image": {"poppler-utils"},
	"pydub":     {"ffmpeg"},
	"pymovie":   {"ffmpeg", "qt5-qmake", "qtbase5-dev", "qtbase5-dev-tools", "qttools5-dev-tools", "libqt5core5a", "python3-pyqt5"},
}
