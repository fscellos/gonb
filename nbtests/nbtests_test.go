package nbtests

// This files has "integration tests": tests that execute notebooks using `nbconvert` which in turn executes
// GoNB as its kernel.
//
// It's a very convenient and easy way to run the tests: it conveniently compiles GoNB binary with --cover (to
// include coverage information) and installs it in a temporary Jupyter configuration location, and includes
// some trivial matching functionality to check for the required output strings, see examples below.
//
// The notebooks used for testing are all in `.../gonb/examples/tests` directory.

import (
	"flag"
	"fmt"
	"github.com/janpfeifer/gonb/kernel"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"
	"os"
	"os/exec"
	"path"
	"testing"
)

var (
	flagPrintNotebook = flag.Bool("print_notebook", false, "print tested notebooks, useful if debugging unexpected results.")
	runArgs           = []string{}
	extraInstallArgs  = []string{"--logtostderr"}
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustValue[T any](v T, err error) T {
	must(err)
	return v
}

func mustRemoveAll(dir string) {
	if dir == "" || dir == "/" {
		return
	}
	must(os.RemoveAll(dir))
}

var (
	rootDir, jupyterDir string
)

// setup for integration tests:
//
//	. Build a gonb binary with --cover (and set GOCOVERDIR).
//	. Set up a temporary jupyter kernel configuration, so that `nbconvert` will use it.
func setup() {
	flag.Parse()
	rootDir = GoNBRootDir()
	if testing.Short() {
		fmt.Println("Test running with --short(), not setting up Jupyter.")
		return
	}

	// Overwrite GOCOVERDIR if $REAL_GOCOVERDIR is given, because
	// -test.gocoverdir value is not propagated.
	// See: https://groups.google.com/g/golang-nuts/c/tg0ZrfpRMSg
	if goCoverDir := os.Getenv("REAL_GOCOVERDIR"); goCoverDir != "" {
		must(os.Setenv("GOCOVERDIR", goCoverDir))
	}

	// Compile and install gonb binary as a local jupyter kernel.
	jupyterDir = mustValue(InstallTmpGonbKernel(runArgs, extraInstallArgs))
	fmt.Printf("%s=%s\n", kernel.JupyterDataDirEnv, jupyterDir)
}

// TestMain is used to set-up / shutdown needed for these integration tests.
func TestMain(m *testing.M) {
	setup()

	// Run tests.
	code := m.Run()

	// Clean up.
	if !testing.Short() {
		mustRemoveAll(jupyterDir)
	}
	os.Exit(code)
}

// executeNotebook (in `examples/tests`) and returns a reader to the output of the execution.
// It executes using `nbconvert` set to `asciidoc` (text) output.
func executedNotebook(t *testing.T, notebook string) *os.File {
	// Prepare output file for nbconvert.
	tmpOutput := mustValue(os.CreateTemp("", "gonb_nbtests_output"))
	nbconvertOutputName := tmpOutput.Name()
	must(tmpOutput.Close())
	must(os.Remove(nbconvertOutputName))
	nbconvertOutputPath := nbconvertOutputName + ".asciidoc" // nbconvert adds this suffix.

	nbconvert := exec.Command(
		"jupyter", "nbconvert", "--to", "asciidoc", "--execute",
		"--output", nbconvertOutputName,
		path.Join(rootDir, "examples", "tests", notebook+".ipynb"))
	nbconvert.Stdout, nbconvert.Stderr = os.Stderr, os.Stdout
	klog.Infof("Executing: %q", nbconvert)
	err := nbconvert.Run()
	require.NoError(t, err)
	f, err := os.Open(nbconvertOutputPath)
	require.NoErrorf(t, err, "Failed to open the output of %q", nbconvert)
	return f
}

func TestHello(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration (nbconvert) test for short tests.")
		return
	}
	f := executedNotebook(t, "hello")
	err := Check(f,
		Match(OutputLine(1),
			Separator,
			"Hello World!",
			Separator),
		*flagPrintNotebook)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
}

func TestFunctions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration (nbconvert) test for short tests.")
		return
	}
	f := executedNotebook(t, "functions")
	err := Check(f,
		Match(
			OutputLine(2),
			Separator,
			"incr: x=2, y=4.14",
			Separator,
		), *flagPrintNotebook)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
}

func TestInit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration (nbconvert) test for short tests.")
		return
	}
	f := executedNotebook(t, "init")
	err := Check(f,
		Sequence(
			Match(
				OutputLine(1),
				Separator,
				"init_a",
				Separator,
			),
			Match(
				OutputLine(2),
				Separator,
				"init_a",
				"init_b",
				Separator,
			),
			Match(
				OutputLine(3),
				Separator,
				"init: v0",
				"init_a",
				"init_b",
				Separator,
			),
			Match(
				OutputLine(4),
				Separator,
				"init: v1",
				"init_a",
				"init_b",
				Separator,
			),
			Match(
				OutputLine(5),
				Separator,
				"removed func init_a",
				"removed func init_b",
				Separator),
			Match(
				OutputLine(6),
				Separator,
				"init: v1",
				"Done",
				Separator,
			),
		),
		*flagPrintNotebook)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
}

// TestGoWork tests support for `go.work` and `%goworkfix` as well as management
// of tracked directories.
func TestGoWork(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration (nbconvert) test for short tests.")
		return
	}
	f := executedNotebook(t, "gowork")
	err := Check(f,
		Sequence(
			Match(
				OutputLine(4),
				Separator,
				`Added replace rule for module "a.com/a/pkg" to local directory`,
				Separator,
			),
			Match(
				OutputLine(5),
				Separator,
				"module gonb_",
				"",
				"go ",
				"",
				"replace a.com/a/pkg => TMP_PKG",
				Separator,
			),
			Match(
				OutputLine(6),
				Separator,
				"List of files/directories being tracked",
				"",
				"/tmp/gonb_tests_gowork_",
				Separator,
			),
			Match(
				OutputLine(8),
				Separator,
				`Untracked "/tmp/gonb_tests_gowork_..."`,
				"",
				"No files or directory being tracked yet",
				Separator,
			),
		), *flagPrintNotebook)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
}

// TestGoFlags tests `%goflags` special command support.
func TestGoFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration (nbconvert) test for short tests.")
		return
	}
	f := executedNotebook(t, "goflags")
	err := Check(f,
		Sequence(
			// Check `%goflags` is correctly keeping/erasing state.
			Match(
				OutputLine(1),
				Separator,
				"%goflags=[\"-cover\"]",
				Separator,
			),
			Match(
				OutputLine(2),
				Separator,
				"%goflags=[\"-cover\"]",
				Separator,
			),
			Match(
				OutputLine(3),
				Separator,
				"%goflags=[]",
				Separator,
			),

			// Check that `-cover` actually had an effect: this it tied to the how go coverage works, and will break
			// the the Go tools change -- probably ok, if it doesn't happen to often.
			// If it does change, just manually run the notebook, see what is the updated output, and if correct,
			// copy over here.
			Match(
				OutputLine(7),
				Separator,
				"A\t\t100.0%",
				"B\t\t0.0%",
			),

			// Check full reset.
			Match(
				OutputLine(8),
				Separator,
				"State reset: all memorized declarations discarded",
				Separator,
			),

			// Check manual running of `go build -gcflags=-m`.
			Match(OutputLine(10), Separator),
			Match("can inline (*Point).ManhattanLen"),
			Match("p does not escape"),
		), *flagPrintNotebook)

	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
}
