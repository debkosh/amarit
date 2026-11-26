module github.com/debkosh/amarit/flatpak

go 1.25

require (
	github.com/godbus/dbus/v5 v5.2.4
	github.com/debkosh/amarit v0.11.1
)

require golang.org/x/sys v0.27.0 // indirect

// Development against the code beside it; a module using this one reads
// the require above.
replace github.com/debkosh/amarit => ../
