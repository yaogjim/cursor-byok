package gui

import (
	"io/fs"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const appName = "日志分析器"

type Resources struct {
	Assets fs.FS
}

func Run(resources Resources) error {
	savedQueryPath, err := DefaultSavedQueryPath()
	if err != nil {
		return err
	}
	service, err := NewService(nil, savedQueryPath)
	if err != nil {
		return err
	}
	app := application.New(application.Options{
		Name:        appName,
		Description: "Cursor BYOK 独立日志分析器",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(resources.Assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyRegular,
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		OnShutdown: service.shutdown,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.cursor-byok.log-analyzer",
		},
	})
	service.setApp(app)
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:               appName,
		Width:               1280,
		Height:              820,
		MinWidth:            980,
		MinHeight:           640,
		URL:                 "/",
		Hidden:              false,
		HideOnEscape:        false,
		MinimiseButtonState: application.ButtonEnabled,
		MaximiseButtonState: application.ButtonEnabled,
		CloseButtonState:    application.ButtonEnabled,
		BackgroundColour:    application.RGBA{Red: 244, Green: 247, Blue: 250, Alpha: 255},
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBar{
				AppearsTransparent:   true,
				HideTitle:            false,
				FullSizeContent:      false,
				HideToolbarSeparator: true,
			},
		},
		Windows: application.WindowsWindow{HiddenOnTaskbar: false},
	})
	return app.Run()
}
