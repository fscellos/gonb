package dom

import (
	"bytes"
	"github.com/janpfeifer/gonb/gonbui"
	"github.com/pkg/errors"
	"text/template"
)

var loadAndRunTmpl = template.Must(template.New("load_and_run").Parse(`
(() => {
	const src="{{.Src}}";
	var runJSFn = function() {
		{{.RunJS}}
	}
	
	var currentScripts = document.head.getElementsByTagName("script");
	for (const idx in currentScripts) {
		let script = currentScripts[idx];
		if (script.src == src) {
			runJSFn();
			return;
		}
	}

	var script = document.createElement("script");
{{range $key, $value := .Attributes}}
	script.{{$key}} = "{{$value}}";
{{end}}	
	script.src = src;
	script.onload = script.onreadystatechange = runJSFn
	document.head.appendChild(script);	
})();
(() => {
	const src="{{.Src}}";
	var runJSFn = function() {
		{{.RunJS}}
	}
	
	var currentScripts = document.head.getElementsByTagName("script");
	for (const idx in currentScripts) {
		let script = currentScripts[idx];
		if (script.src == src) {
			runJSFn();
			return;
		}
	}

	var script = document.createElement("script");
{{range $key, $value := .Attributes}}
	script.{{$key}} = "{{$value}}";
{{end}}	
	script.src = src;
	script.onload = script.onreadystatechange = runJSFn
	document.head.appendChild(script);	
})();
`))

// LoadScriptModuleAndRun loads the given script module and, `onLoad`, runs the given code.
//
// If the module has been previously loaded, it immediately runs the given code.
//
// The script module given is appended to the `HEAD` of the page.
//
// Extra `attributes` can be given, and will be appended to the `script` node.
//
// Example: to make sure Plotly Javascript (https://plotly.com/javascript/) is loaded --
// please check Plotly's installation directions for the latest version.
//
//	gonbui.LoadScriptModuleAndRun(
//		"https://cdn.plot.ly/plotly-2.29.1.min.js", {"charset": "utf-8"},
//		"console.log('Plotly loaded.')");
func LoadScriptModuleAndRun(src string, attributes map[string]string, runJS string) error {
	var buf bytes.Buffer
	data := struct {
		Src, RunJS string
		Attributes map[string]string
	}{
		Src:        src,
		RunJS:      runJS,
		Attributes: attributes,
	}
	err := loadAndRunTmpl.Execute(&buf, data)
	if err != nil {
		return errors.Wrapf(err, "failed to execut template for LoadScriptModuleRun()")
	}
	js := buf.String()
	gonbui.ScriptJavascript(js)
	return nil
}

var loadOrRequireAndRunTmpl = template.Must(template.New("load_or_required_and_run").Parse(`
(() => {
	const src="{{.Src}}";
	var runJSFn = function(module) {
		{{.RunJS}}
	}
	
    if (typeof requirejs === "function") {
        // Use RequireJS to load module.
		let srcWithoutExtension = src.substring(0, src.lastIndexOf(".js"));
        requirejs.config({
            paths: {
                '{{.ModuleName}}': srcWithoutExtension
            }
        });
        require(['{{.ModuleName}}'], function({{.ModuleName}}) {
            runJSFn({{.ModuleName}})
        });
        return
    }

	var currentScripts = document.head.getElementsByTagName("script");
	for (const idx in currentScripts) {
		let script = currentScripts[idx];
		if (script.src == src) {
			runJSFn(null);
			return;
		}
	}

	var script = document.createElement("script");
{{range $key, $value := .Attributes}}
	script.{{$key}} = "{{$value}}";
{{end}}	
	script.src = src;
	script.onload = script.onreadystatechange = function () { runJSFn(null); };
	document.head.appendChild(script);	
})();
`))

// LoadScriptOrRequireJSModuleAndRun is similar to [LoadScriptModuleAndRun] but it will use RequireJS if loaded,
// and it uses DisplayHtml instead -- which allows it to be included if the notebook is exported.
//
// In this version `runJS` will have `module` defined as the name of the module passed by `require` is RequireJS is
// available, or have it set to `null` otherwise.
//
// Notice while Jupyter notebook uses RequireJS, it hides in its context, so for the cells' HTML content, it is as
// if RequireJS is not available. But when the notebook is exported to HTML, RequireJS is available.
// LoadScriptOrRequireJSModuleAndRun will issue javascript code that dynamically handles both situations.
//
// Args:
//   - `moduleName`: is the name to be module to be used if RequireJS is installed -- it is ignored if RequireJS is not
//     available.
//   - `src`: URL of the library to load. Used as the script source if loading the script the usual way, or used
//     as the paths configuration option for RequireJS.
//   - `attributes`: Extra attributes to use in the `<script>` tag, if RequestJS is not available.
//   - `runJS`: Javascript code to run, where `module` will be defined to the imported module if RequireJS is installed,
//     and `null` otherwise.
func LoadScriptOrRequireJSModuleAndRun(moduleName, src string, attributes map[string]string, runJS string) error {
	return loadScriptOrRequireJSModuleAndRunImpl(moduleName, src, attributes, runJS, false)
}

// LoadScriptOrRequireJSModuleAndRunTransient works exactly like [LoadScriptOrRequireJSModuleAndRun], but the javascript
// code is executed as transient content (not saved).
func LoadScriptOrRequireJSModuleAndRunTransient(moduleName, src string, attributes map[string]string, runJS string) error {
	return loadScriptOrRequireJSModuleAndRunImpl(moduleName, src, attributes, runJS, true)
}

func loadScriptOrRequireJSModuleAndRunImpl(moduleName, src string, attributes map[string]string, runJS string, transient bool) error {
	var buf bytes.Buffer
	data := struct {
		ModuleName, Src, RunJS string
		Attributes             map[string]string
	}{
		ModuleName: moduleName,
		Src:        src,
		RunJS:      runJS,
		Attributes: attributes,
	}
	err := loadOrRequireAndRunTmpl.Execute(&buf, data)
	if err != nil {
		return errors.Wrapf(err, "failed to execute template for LoadScriptOrRequireJSModuleAndRun(%q)", moduleName)
	}
	js := buf.String()
	if transient {
		TransientJavascript(js)
	} else {
		gonbui.DisplayHTMLF("<script charset=%q>%s</script>", "UTF-8", js)
	}
	return nil
}
