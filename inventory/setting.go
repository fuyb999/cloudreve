package inventory

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/setting"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/cache"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/gofrs/uuid"
)

type (
	SettingClient interface {
		TxOperator
		// Get gets a setting value from DB, returns error if setting cannot be found.
		Get(ctx context.Context, name string) (string, error)
		// Set sets a setting value to DB.
		Set(ctx context.Context, settings map[string]string) error
		// Gets gets multiple setting values from DB, returns error if any setting cannot be found.
		Gets(ctx context.Context, names []string) (map[string]string, error)
	}
)

// NewSettingClient creates a new SettingClient
func NewSettingClient(client *ent.Client, kv cache.Driver) SettingClient {
	return &settingClient{client: client, kv: kv}
}

type settingClient struct {
	client *ent.Client
	kv     cache.Driver
}

// SetClient sets the client for the setting client
func (c *settingClient) SetClient(newClient *ent.Client) TxOperator {
	return &settingClient{client: newClient, kv: c.kv}
}

// GetClient gets the client for the setting client
func (c *settingClient) GetClient() *ent.Client {
	return c.client
}

func (c *settingClient) Get(ctx context.Context, name string) (string, error) {
	s, err := c.client.Setting.Query().Where(setting.Name(name)).Only(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to query setting %q from DB: %w", name, err)
	}

	return s.Value, nil
}

func (c *settingClient) Gets(ctx context.Context, names []string) (map[string]string, error) {
	settings := make(map[string]string)
	res, err := c.client.Setting.Query().Where(setting.NameIn(names...)).All(ctx)
	if err != nil {
		return nil, err
	}

	for _, s := range res {
		settings[s.Name] = s.Value
	}

	return settings, nil
}

func (c *settingClient) Set(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		if err := c.client.Setting.Create().
			SetName(k).
			SetValue(v).
			OnConflictColumns(setting.FieldName).
			UpdateNewValues().
			Exec(ctx); err != nil {
			return fmt.Errorf("failed to upsert setting %q: %w", k, err)
		}
	}

	return nil
}

var (
	defaultArchiveViewerExts = []string{
		"zip", "7z", "rar", "tar", "tgz", "tbz", "tbz2", "txz", "tlz",
		"gz", "z", "zz", "bz", "bz2", "xz", "lz", "lz4", "lzma", "br", "sz", "zst",
		"jar", "war", "ear", "ar", "cpio", "arj",
	}

	defaultIcons = []types.FileTypeIconSetting{
		{
			Exts:  []string{"mp3", "flac", "ape", "wav", "acc", "ogg", "m4a"},
			Icon:  "audio",
			Color: "#651fff",
		},
		{
			Exts:  []string{"m3u8", "mp4", "flv", "avi", "wmv", "mkv", "rm", "rmvb", "mov", "ogv"},
			Icon:  "video",
			Color: "#d50000",
		},
		{
			Exts:  []string{"bmp", "iff", "png", "gif", "jpg", "jpeg", "psd", "svg", "webp", "heif", "heic", "tiff", "avif"},
			Icon:  "image",
			Color: "#d32f2f",
		},
		{
			Exts:  []string{"3fr", "ari", "arw", "bay", "braw", "crw", "cr2", "cr3", "cap", "dcs", "dcr", "dng", "drf", "eip", "erf", "fff", "gpr", "iiq", "k25", "kdc", "mdc", "mef", "mos", "mrw", "nef", "nrw", "obm", "orf", "pef", "ptx", "pxn", "r3d", "raf", "raw", "rwl", "rw2", "rwz", "sr2", "srf", "srw", "tif", "x3f"},
			Icon:  "raw",
			Color: "#d32f2f",
		},
		{
			Exts:  []string{"pdf"},
			Color: "#f44336",
			Icon:  "pdf",
		},
		{
			Exts:  []string{"doc", "docx"},
			Color: "#538ce5",
			Icon:  "word",
		},
		{
			Exts:  []string{"ppt", "pptx"},
			Color: "#EF633F",
			Icon:  "ppt",
		},
		{
			Exts:  []string{"xls", "xlsx", "csv"},
			Color: "#4caf50",
			Icon:  "excel",
		},
		{
			Exts:  []string{"txt", "html"},
			Color: "#607d8b",
			Icon:  "text",
		},
		{
			Exts:  []string{"torrent"},
			Color: "#5c6bc0",
			Icon:  "torrent",
		},
		{
			Exts:  defaultArchiveViewerExts,
			Color: "#f9a825",
			Icon:  "zip",
		},
		{
			Exts:  []string{"exe", "msi"},
			Color: "#1a237e",
			Icon:  "exe",
		},
		{
			Exts:  []string{"apk"},
			Color: "#8bc34a",
			Icon:  "android",
		},
		{
			Exts:  []string{"go"},
			Color: "#16b3da",
			Icon:  "go",
		},
		{
			Exts:  []string{"py"},
			Color: "#3776ab",
			Icon:  "python",
		},
		{
			Exts:  []string{"c"},
			Color: "#a4c639",
			Icon:  "c",
		},
		{
			Exts:  []string{"cpp"},
			Color: "#f34b7d",
			Icon:  "cpp",
		},
		{
			Exts:  []string{"js", "jsx"},
			Color: "#f4d003",
			Icon:  "js",
		},
		{
			Exts:  []string{"epub"},
			Color: "#81b315",
			Icon:  "book",
		},
		{
			Exts:      []string{"rs"},
			Color:     "#000",
			ColorDark: "#fff",
			Icon:      "rust",
		},
		{
			Exts:  []string{"drawio"},
			Color: "#F08705",
			Icon:  "flowchart",
		},
		{
			Exts:  []string{"dwb"},
			Color: "#F08705",
			Icon:  "whiteboard",
		},
		{
			Exts:      []string{"md"},
			Color:     "#383838",
			ColorDark: "#cbcbcb",
			Icon:      "markdown",
		},
		{
			Img:  "/static/img/viewers/excalidraw.svg",
			Exts: []string{"excalidraw"},
		},
	}

	defaultFileViewers = []types.ViewerGroup{
		{
			Viewers: []types.Viewer{
				{
					ID:          "music",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.musicPlayer",
					Exts:        []string{"mp3", "ogg", "wav", "flac", "m4a"},
				},
				{
					ID:          "epub",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.epubViewer",
					Exts:        []string{"epub"},
				},
				{
					ID:          "googledocs",
					Type:        types.ViewerTypeCustom,
					DisplayName: "fileManager.googledocs",
					Icon:        "/static/img/viewers/gdrive.png",
					Url:         "https://docs.google.com/gview?url={$src}&embedded=true",
					Exts:        []string{"jpeg", "png", "gif", "tiff", "bmp", "webm", "mpeg4", "3gpp", "mov", "avi", "mpegps", "wmv", "flv", "txt", "css", "html", "php", "c", "cpp", "h", "hpp", "js", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "pdf", "pages", "ai", "psd", "tiff", "dxf", "svg", "eps", "ps", "ttf", "xps"},
					MaxSize:     26214400,
				},
				{
					ID:          "m365online",
					Type:        types.ViewerTypeCustom,
					DisplayName: "fileManager.m365viewer",
					Icon:        "/static/img/viewers/m365.svg",
					Url:         "https://view.officeapps.live.com/op/view.aspx?src={$src}",
					Exts:        []string{"doc", "docx", "docm", "dotm", "dotx", "xlsx", "xlsb", "xls", "xlsm", "pptx", "ppsx", "ppt", "pps", "pptm", "potm", "ppam", "potx", "ppsm"},
					MaxSize:     10485760,
				},
				{
					ID:          "pdf",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.pdfViewer",
					Exts:        []string{"pdf"},
				},
				{
					ID:          "video",
					Type:        types.ViewerTypeBuiltin,
					Icon:        "/static/img/viewers/artplayer.png",
					DisplayName: "Artplayer",
					Exts:        []string{"mp4", "mkv", "webm", "avi", "mov", "m3u8", "flv"},
				},
				{
					ID:          "markdown",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.markdownEditor",
					Exts:        []string{"md"},
					Templates: []types.NewFileTemplate{
						{
							Ext:         "md",
							DisplayName: "Markdown",
						},
					},
				},
				{
					ID:          "drawio",
					Type:        types.ViewerTypeBuiltin,
					Icon:        "/static/img/viewers/drawio.svg",
					DisplayName: "draw.io",
					Exts:        []string{"drawio", "dwb"},
					Props: map[string]string{
						"host": "https://embed.diagrams.net",
					},
					Templates: []types.NewFileTemplate{
						{
							Ext:         "drawio",
							DisplayName: "fileManager.diagram",
						},
						{
							Ext:         "dwb",
							DisplayName: "fileManager.whiteboard",
						},
					},
				},
				{
					ID:          "image",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.imageViewer",
					Exts:        []string{"bmp", "png", "gif", "jpg", "jpeg", "svg", "webp", "heic", "heif"},
				},
				{
					ID:          "monaco",
					Type:        types.ViewerTypeBuiltin,
					Icon:        "/static/img/viewers/monaco.svg",
					DisplayName: "fileManager.monacoEditor",
					Exts:        []string{"md", "txt", "json", "php", "py", "bat", "c", "h", "cpp", "hpp", "cs", "css", "dockerfile", "go", "html", "htm", "ini", "java", "js", "jsx", "less", "lua", "sh", "sql", "xml", "yaml"},
					Templates: []types.NewFileTemplate{
						{
							Ext:         "txt",
							DisplayName: "fileManager.text",
						},
					},
				},
				{
					ID:          "photopea",
					Type:        types.ViewerTypeBuiltin,
					Icon:        "/static/img/viewers/photopea.png",
					DisplayName: "Photopea",
					Exts:        []string{"psd", "ai", "indd", "xcf", "xd", "fig", "kri", "clip", "pxd", "pxz", "cdr", "ufo", "afphoyo", "svg", "esp", "pdf", "pdn", "wmf", "emf", "png", "jpg", "jpeg", "gif", "webp", "ico", "icns", "bmp", "avif", "heic", "jxl", "ppm", "pgm", "pbm", "tiff", "dds", "iff", "anim", "tga", "dng", "nef", "cr2", "cr3", "arw", "rw2", "raf", "orf", "gpr", "3fr", "fff"},
				},
				{
					ID:          "excalidraw",
					Type:        types.ViewerTypeBuiltin,
					Icon:        "/static/img/viewers/excalidraw.svg",
					DisplayName: "Excalidraw",
					Exts:        []string{"excalidraw"},
					Templates: []types.NewFileTemplate{
						{
							Ext:         "excalidraw",
							DisplayName: "Excalidraw",
						},
					},
				},
				{
					ID:          "archive",
					Type:        types.ViewerTypeBuiltin,
					DisplayName: "fileManager.archivePreview",
					Exts:        defaultArchiveViewerExts,
					RequiredGroupPermission: []types.GroupPermission{
						types.GroupPermissionArchiveTask,
					},
				},
			},
		},
	}

	defaultFileProps = []types.CustomProps{
		{
			ID:   "description",
			Type: types.CustomPropsTypeText,
			Name: "fileManager.description",
			Icon: "fluent:slide-text-24-filled",
		},
		{
			ID:   "rating",
			Type: types.CustomPropsTypeRating,
			Name: "fileManager.rating",
			Icon: "fluent:data-bar-vertical-star-24-filled",
			Max:  5,
		},
	}

	defaultActiveMailBody = `<html lang=[[ .Language ]] xmlns=http://www.w3.org/1999/xhtml xmlns:o=urn:schemas-microsoft-com:office:office xmlns:v=urn:schemas-microsoft-com:vml><title></title><meta charset=UTF-8><meta content="text/html; charset=UTF-8"http-equiv=Content-Type><!--[if !mso]>--><meta content="IE=edge"http-equiv=X-UA-Compatible><!--<![endif]--><meta content=""name=x-apple-disable-message-reformatting><meta content="target-densitydpi=device-dpi"name=viewport><meta content=true name=HandheldFriendly><meta content="width=device-width"name=viewport><meta content="telephone=no, date=no, address=no, email=no, url=no"name=format-detection><style>table{border-collapse:separate;table-layout:fixed;mso-table-lspace:0;mso-table-rspace:0}table td{border-collapse:collapse}.ExternalClass{width:100%}.ExternalClass,.ExternalClass div,.ExternalClass font,.ExternalClass p,.ExternalClass span,.ExternalClass td{line-height:100%}a,body,h1,h2,h3,li,p{-ms-text-size-adjust:100%;-webkit-text-size-adjust:100%}html{-webkit-text-size-adjust:none!important}#innerTable,body{-webkit-font-smoothing:antialiased;-moz-osx-font-smoothing:grayscale}#innerTable img+div{display:none;display:none!important}img{Margin:0;padding:0;-ms-interpolation-mode:bicubic}a,h1,h2,h3,p{line-height:inherit;overflow-wrap:normal;white-space:normal;word-break:break-word}a{text-decoration:none}h1,h2,h3,p{min-width:100%!important;width:100%!important;max-width:100%!important;display:inline-block!important;border:0;padding:0;margin:0}a[x-apple-data-detectors]{color:inherit!important;text-decoration:none!important;font-size:inherit!important;font-family:inherit!important;font-weight:inherit!important;line-height:inherit!important}u+#body a{color:inherit;text-decoration:none;font-size:inherit;font-family:inherit;font-weight:inherit;line-height:inherit}a[href^=mailto],a[href^=sms],a[href^=tel]{color:inherit;text-decoration:none}</style><style>@media (min-width:481px){.hd{display:none!important}}</style><style>@media (max-width:480px){.hm{display:none!important}}</style><style>@media (max-width:480px){.t41,.t46{mso-line-height-alt:0!important;line-height:0!important;display:none!important}.t42{padding:40px!important}.t44{border-radius:0!important;width:480px!important}.t15,.t39,.t9{width:398px!important}.t32{text-align:left!important}.t25{display:revert!important}.t27,.t31{vertical-align:top!important;width:auto!important;max-width:100%!important}}</style><!--[if !mso]>--><link href="https://fonts.googleapis.com/css2?family=Montserrat:wght@700&family=Sofia+Sans:wght@700&family=Open+Sans:wght@400;500;600&display=swap"rel=stylesheet><!--<![endif]--><!--[if mso]><xml><o:officedocumentsettings><o:allowpng><o:pixelsperinch>96</o:pixelsperinch></o:officedocumentsettings></xml><![endif]--><body class=t49 id=body style=min-width:100%;Margin:0;padding:0;background-color:#fff><div style=background-color:#fff class=t48><table cellpadding=0 cellspacing=0 role=presentation align=center border=0 width=100%><tr><td class=t47 style=font-size:0;line-height:0;mso-line-height-rule:exactly;background-color:#fff align=center valign=top><!--[if mso]><v:background xmlns:v=urn:schemas-microsoft-com:vml fill=true stroke=false><v:fill color=#FFFFFF></v:background><![endif]--><table cellpadding=0 cellspacing=0 role=presentation align=center border=0 width=100% id=innerTable><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:50px;line-height:50px;font-size:1px;display:block class=t41>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t45 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t44 style="background-color:#fff;border:1px solid #ebebeb;overflow:hidden;width:600px;border-radius:12px 12px 12px 12px"width=600><![endif]--><!--[if !mso]>--><td class=t44 style="background-color:#fff;border:1px solid #ebebeb;overflow:hidden;width:600px;border-radius:12px 12px 12px 12px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t43 style=width:100% width=100%><tr><td class=t42 style="padding:44px 42px 32px 42px"><table cellpadding=0 cellspacing=0 role=presentation style=width:100%!important width=100%><tr><td align=left><table cellpadding=0 cellspacing=0 role=presentation class=t4 style=Margin-right:auto><tr><!--[if mso]><td class=t3 style=width:42px width=42><![endif]--><!--[if !mso]>--><td class=t3 style=width:100px><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t2 style=width:100% width=100%><tr><td class=t1><div style=font-size:0><a href="{{ .CommonContext.SiteUrl }}"><img alt=""class=t0 height=100 src="{{ .CommonContext.Logo.Normal }}"style=display:block;border:0;height:auto;width:100%;Margin:0;max-width:100%></a></div></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:22px;line-height:22px;font-size:1px;display:block class=t5>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t10 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t9 style="border-bottom:1px solid #eff1f4;width:514px"width=514><![endif]--><!--[if !mso]>--><td class=t9 style="border-bottom:1px solid #eff1f4;width:514px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t8 style=width:100% width=100%><tr><td class=t7 style="padding:0 0 18px 0"><h1 class=t6 style="margin:0;Margin:0;font-family:Montserrat,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:28px;font-weight:700;font-style:normal;font-size:24px;text-decoration:none;text-transform:none;letter-spacing:-1px;direction:ltr;color:#141414;text-align:left;mso-line-height-rule:exactly;mso-text-raise:1px">[[ .ActiveTitle ]]</h1></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:18px;line-height:18px;font-size:1px;display:block class=t11>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t16 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t15 style=width:514px width=514><![endif]--><!--[if !mso]>--><td class=t15 style=width:514px><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t14 style=width:100% width=100%><tr><td class=t13><p class=t12 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:25px;font-weight:400;font-style:normal;font-size:15px;text-decoration:none;text-transform:none;letter-spacing:-.1px;direction:ltr;color:#141414;text-align:left;mso-line-height-rule:exactly;mso-text-raise:3px">[[ .ActiveDes ]]</table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:24px;line-height:24px;font-size:1px;display:block class=t18>  </div><tr><td align=left><a href="{{ .Url }}"><table cellpadding=0 cellspacing=0 role=presentation class=t22 style=margin-right:auto><tr><!--[if mso]><td class=t21 style="background-color:#0666eb;overflow:hidden;width:auto;border-radius:40px 40px 40px 40px"><![endif]--><!--[if !mso]>--><td class=t21 style="background-color:#0666eb;overflow:hidden;width:auto;border-radius:40px 40px 40px 40px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t20 style=width:auto><tr><td class=t19 style="line-height:34px;mso-line-height-rule:exactly;mso-text-raise:5px;padding:0 23px 0 23px"><span class=t17 style="display:block;margin:0;Margin:0;font-family:Sofia Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:34px;font-weight:700;font-style:normal;font-size:16px;text-decoration:none;text-transform:none;letter-spacing:-.2px;direction:ltr;color:#fff;mso-line-height-rule:exactly;mso-text-raise:5px">[[ .ActiveButton ]]</span></table></table></a><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:40px;line-height:40px;font-size:1px;display:block class=t36>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t40 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t39 style="border-top:1px solid #dfe1e4;width:514px"width=514><![endif]--><!--[if !mso]>--><td class=t39 style="border-top:1px solid #dfe1e4;width:514px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t38 style=width:100% width=100%><tr><td class=t37 style="padding:24px 0 0 0"><div style=width:100%;text-align:left class=t35><div style=display:inline-block class=t34><table cellpadding=0 cellspacing=0 role=presentation class=t33 align=left valign=top><tr class=t32><td><td class=t27 valign=top><table cellpadding=0 cellspacing=0 role=presentation class=t26 style=width:auto width=100%><tr><td class=t24 style=background-color:#fff;line-height:20px;mso-line-height-rule:exactly;mso-text-raise:2px><span class=t23 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:20px;font-weight:600;font-style:normal;font-size:14px;text-decoration:none;direction:ltr;color:#222;mso-line-height-rule:exactly;mso-text-raise:2px">{{ .CommonContext.SiteBasic.Name }}</span> <span class=t28 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:20px;font-weight:500;font-style:normal;font-size:14px;text-decoration:none;direction:ltr;color:#b4becc;mso-line-height-rule:exactly;mso-text-raise:2px;margin-left:8px">[[ .EmailIsAutoSend ]]</span><td class=t25 style=width:20px width=20></table><td></table></div></div></table></table></table></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:50px;line-height:50px;font-size:1px;display:block class=t46>  </div></table></table></div><div style="display:none;white-space:nowrap;font:15px courier;line-height:0"class=gmail-fix>                                                           </div>`
	defaultResetMailBody  = `<html lang=[[ .Language ]] xmlns=http://www.w3.org/1999/xhtml xmlns:o=urn:schemas-microsoft-com:office:office xmlns:v=urn:schemas-microsoft-com:vml><title></title><meta charset=UTF-8><meta content="text/html; charset=UTF-8"http-equiv=Content-Type><!--[if !mso]>--><meta content="IE=edge"http-equiv=X-UA-Compatible><!--<![endif]--><meta content=""name=x-apple-disable-message-reformatting><meta content="target-densitydpi=device-dpi"name=viewport><meta content=true name=HandheldFriendly><meta content="width=device-width"name=viewport><meta content="telephone=no, date=no, address=no, email=no, url=no"name=format-detection><style>table{border-collapse:separate;table-layout:fixed;mso-table-lspace:0;mso-table-rspace:0}table td{border-collapse:collapse}.ExternalClass{width:100%}.ExternalClass,.ExternalClass div,.ExternalClass font,.ExternalClass p,.ExternalClass span,.ExternalClass td{line-height:100%}a,body,h1,h2,h3,li,p{-ms-text-size-adjust:100%;-webkit-text-size-adjust:100%}html{-webkit-text-size-adjust:none!important}#innerTable,body{-webkit-font-smoothing:antialiased;-moz-osx-font-smoothing:grayscale}#innerTable img+div{display:none;display:none!important}img{Margin:0;padding:0;-ms-interpolation-mode:bicubic}a,h1,h2,h3,p{line-height:inherit;overflow-wrap:normal;white-space:normal;word-break:break-word}a{text-decoration:none}h1,h2,h3,p{min-width:100%!important;width:100%!important;max-width:100%!important;display:inline-block!important;border:0;padding:0;margin:0}a[x-apple-data-detectors]{color:inherit!important;text-decoration:none!important;font-size:inherit!important;font-family:inherit!important;font-weight:inherit!important;line-height:inherit!important}u+#body a{color:inherit;text-decoration:none;font-size:inherit;font-family:inherit;font-weight:inherit;line-height:inherit}a[href^=mailto],a[href^=sms],a[href^=tel]{color:inherit;text-decoration:none}</style><style>@media (min-width:481px){.hd{display:none!important}}</style><style>@media (max-width:480px){.hm{display:none!important}}</style><style>@media (max-width:480px){.t41,.t46{mso-line-height-alt:0!important;line-height:0!important;display:none!important}.t42{padding:40px!important}.t44{border-radius:0!important;width:480px!important}.t15,.t39,.t9{width:398px!important}.t32{text-align:left!important}.t25{display:revert!important}.t27,.t31{vertical-align:top!important;width:auto!important;max-width:100%!important}}</style><!--[if !mso]>--><link href="https://fonts.googleapis.com/css2?family=Montserrat:wght@700&family=Sofia+Sans:wght@700&family=Open+Sans:wght@400;500;600&display=swap"rel=stylesheet><!--<![endif]--><!--[if mso]><xml><o:officedocumentsettings><o:allowpng><o:pixelsperinch>96</o:pixelsperinch></o:officedocumentsettings></xml><![endif]--><body class=t49 id=body style=min-width:100%;Margin:0;padding:0;background-color:#fff><div style=background-color:#fff class=t48><table cellpadding=0 cellspacing=0 role=presentation align=center border=0 width=100%><tr><td class=t47 style=font-size:0;line-height:0;mso-line-height-rule:exactly;background-color:#fff align=center valign=top><!--[if mso]><v:background xmlns:v=urn:schemas-microsoft-com:vml fill=true stroke=false><v:fill color=#FFFFFF></v:background><![endif]--><table cellpadding=0 cellspacing=0 role=presentation align=center border=0 width=100% id=innerTable><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:50px;line-height:50px;font-size:1px;display:block class=t41>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t45 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t44 style="background-color:#fff;border:1px solid #ebebeb;overflow:hidden;width:600px;border-radius:12px 12px 12px 12px"width=600><![endif]--><!--[if !mso]>--><td class=t44 style="background-color:#fff;border:1px solid #ebebeb;overflow:hidden;width:600px;border-radius:12px 12px 12px 12px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t43 style=width:100% width=100%><tr><td class=t42 style="padding:44px 42px 32px 42px"><table cellpadding=0 cellspacing=0 role=presentation style=width:100%!important width=100%><tr><td align=left><table cellpadding=0 cellspacing=0 role=presentation class=t4 style=Margin-right:auto><tr><!--[if mso]><td class=t3 style=width:42px width=42><![endif]--><!--[if !mso]>--><td class=t3 style=width:100px><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t2 style=width:100% width=100%><tr><td class=t1><div style=font-size:0><a href="{{ .CommonContext.SiteUrl }}"><img alt=""class=t0 height=100 src="{{ .CommonContext.Logo.Normal }}"style=display:block;border:0;height:auto;width:100%;Margin:0;max-width:100%></a></div></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:22px;line-height:22px;font-size:1px;display:block class=t5>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t10 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t9 style="border-bottom:1px solid #eff1f4;width:514px"width=514><![endif]--><!--[if !mso]>--><td class=t9 style="border-bottom:1px solid #eff1f4;width:514px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t8 style=width:100% width=100%><tr><td class=t7 style="padding:0 0 18px 0"><h1 class=t6 style="margin:0;Margin:0;font-family:Montserrat,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:28px;font-weight:700;font-style:normal;font-size:24px;text-decoration:none;text-transform:none;letter-spacing:-1px;direction:ltr;color:#141414;text-align:left;mso-line-height-rule:exactly;mso-text-raise:1px">[[ .ResetTitle ]]</h1></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:18px;line-height:18px;font-size:1px;display:block class=t11>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t16 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t15 style=width:514px width=514><![endif]--><!--[if !mso]>--><td class=t15 style=width:514px><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t14 style=width:100% width=100%><tr><td class=t13><p class=t12 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:25px;font-weight:400;font-style:normal;font-size:15px;text-decoration:none;text-transform:none;letter-spacing:-.1px;direction:ltr;color:#141414;text-align:left;mso-line-height-rule:exactly;mso-text-raise:3px">[[ .ResetDes ]]</table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:24px;line-height:24px;font-size:1px;display:block class=t18>  </div><tr><td align=left><a href="{{ .Url }}"><table cellpadding=0 cellspacing=0 role=presentation class=t22 style=margin-right:auto><tr><!--[if mso]><td class=t21 style="background-color:#0666eb;overflow:hidden;width:auto;border-radius:40px 40px 40px 40px"><![endif]--><!--[if !mso]>--><td class=t21 style="background-color:#0666eb;overflow:hidden;width:auto;border-radius:40px 40px 40px 40px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t20 style=width:auto><tr><td class=t19 style="line-height:34px;mso-line-height-rule:exactly;mso-text-raise:5px;padding:0 23px 0 23px"><span class=t17 style="display:block;margin:0;Margin:0;font-family:Sofia Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:34px;font-weight:700;font-style:normal;font-size:16px;text-decoration:none;text-transform:none;letter-spacing:-.2px;direction:ltr;color:#fff;mso-line-height-rule:exactly;mso-text-raise:5px">[[ .ResetButton ]]</span></table></table></a><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:40px;line-height:40px;font-size:1px;display:block class=t36>  </div><tr><td align=center><table cellpadding=0 cellspacing=0 role=presentation class=t40 style=Margin-left:auto;Margin-right:auto><tr><!--[if mso]><td class=t39 style="border-top:1px solid #dfe1e4;width:514px"width=514><![endif]--><!--[if !mso]>--><td class=t39 style="border-top:1px solid #dfe1e4;width:514px"><!--<![endif]--><table cellpadding=0 cellspacing=0 role=presentation class=t38 style=width:100% width=100%><tr><td class=t37 style="padding:24px 0 0 0"><div style=width:100%;text-align:left class=t35><div style=display:inline-block class=t34><table cellpadding=0 cellspacing=0 role=presentation class=t33 align=left valign=top><tr class=t32><td><td class=t27 valign=top><table cellpadding=0 cellspacing=0 role=presentation class=t26 style=width:auto width=100%><tr><td class=t24 style=background-color:#fff;line-height:20px;mso-line-height-rule:exactly;mso-text-raise:2px><span class=t23 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:20px;font-weight:600;font-style:normal;font-size:14px;text-decoration:none;direction:ltr;color:#222;mso-line-height-rule:exactly;mso-text-raise:2px">{{ .CommonContext.SiteBasic.Name }}</span> <span class=t28 style="margin:0;Margin:0;font-family:Open Sans,BlinkMacSystemFont,Segoe UI,Helvetica Neue,Arial,sans-serif;line-height:20px;font-weight:500;font-style:normal;font-size:14px;text-decoration:none;direction:ltr;color:#b4becc;mso-line-height-rule:exactly;mso-text-raise:2px;margin-left:8px">[[ .EmailIsAutoSend ]]</span><td class=t25 style=width:20px width=20></table><td></table></div></div></table></table></table></table></table><tr><td><div style=mso-line-height-rule:exactly;mso-line-height-alt:50px;line-height:50px;font-size:1px;display:block class=t46>  </div></table></table></div><div style="display:none;white-space:nowrap;font:15px courier;line-height:0"class=gmail-fix>                                                           </div>`
)

type MailTemplateContent struct {
	Language        string
	EmailIsAutoSend string // Translation of `此邮件由系统自动发送。`

	ActiveTitle  string // Translation of `激活你的账号`
	ActiveDes    string // Translation of `请点击下方按钮确认你的电子邮箱并完成账号注册，此链接有效期为 24 小时。`
	ActiveButton string // Translation of `确认激活`

	ResetTitle  string // Translation of `重设密码`
	ResetDes    string // Translation of `请点击下方按钮重设你的密码，此链接有效期为 1 小时。`
	ResetButton string // Translation of `重设密码`
}

var mailTemplateContents = []MailTemplateContent{
	{
		Language:        "en-US",
		EmailIsAutoSend: "This email is sent automatically.",
		ActiveTitle:     "Confirm your account",
		ActiveDes:       "Please click the button below to confirm your email address and finish setting up your account. This link is valid for 24 hours.",
		ActiveButton:    "Confirm",
		ResetTitle:      "Reset your password",
		ResetDes:        "Please click the button below to reset your password. This link is valid for 1 hour.",
		ResetButton:     "Reset",
	},
	{
		Language:        "zh-CN",
		EmailIsAutoSend: "此邮件由系统自动发送。",
		ActiveTitle:     "激活你的账号",
		ActiveDes:       "请点击下方按钮确认你的电子邮箱并完成账号注册，此链接有效期为 24 小时。",
		ActiveButton:    "确认激活",
		ResetTitle:      "重设密码",
		ResetDes:        "请点击下方按钮重设你的密码，此链接有效期为 1 小时。",
		ResetButton:     "重设密码",
	},
	{
		Language:        "zh-TW",
		EmailIsAutoSend: "此郵件由系統自動發送。",
		ActiveTitle:     "激活你的帳號",
		ActiveDes:       "請點擊下方按鈕確認你的電子郵箱並完成帳號註冊，此連結有效期為 24 小時。",
		ActiveButton:    "確認激活",
		ResetTitle:      "重設密碼",
		ResetDes:        "請點擊下方按鈕重設你的密碼，此連結有效期為 1 小時。",
		ResetButton:     "重設密碼",
	},
	{
		Language:        "de-DE",
		EmailIsAutoSend: "Diese E-Mail wird automatisch vom System gesendet.",
		ActiveTitle:     "Bestätigen Sie Ihr Konto",
		ActiveDes:       "Bitte klicken Sie auf die Schaltfläche unten, um Ihre E-Mail-Adresse zu bestätigen und Ihr Konto einzurichten. Dieser Link ist 24 Stunden lang gültig.",
		ActiveButton:    "Bestätigen",
		ResetTitle:      "Passwort zurücksetzen",
		ResetDes:        "Bitte klicken Sie auf die Schaltfläche unten, um Ihr Passwort zurückzusetzen. Dieser Link ist 1 Stunde lang gültig.",
		ResetButton:     "Passwort zurücksetzen",
	},
	{
		Language:        "es-ES",
		EmailIsAutoSend: "Este correo electrónico se envía automáticamente.",
		ActiveTitle:     "Confirma tu cuenta",
		ActiveDes:       "Por favor, haz clic en el botón de abajo para confirmar tu dirección de correo electrónico y completar la configuración de tu cuenta. Este enlace es válido por 24 horas.",
		ActiveButton:    "Confirmar",
		ResetTitle:      "Restablecer tu contraseña",
		ResetDes:        "Por favor, haz clic en el botón de abajo para restablecer tu contraseña. Este enlace es válido por 1 hora.",
		ResetButton:     "Restablecer",
	},
	{
		Language:        "fr-FR",
		EmailIsAutoSend: "Cet e-mail est envoyé automatiquement.",
		ActiveTitle:     "Confirmer votre compte",
		ActiveDes:       "Veuillez cliquer sur le bouton ci-dessous pour confirmer votre adresse e-mail et terminer la configuration de votre compte. Ce lien est valable 24 heures.",
		ActiveButton:    "Confirmer",
		ResetTitle:      "Réinitialiser votre mot de passe",
		ResetDes:        "Veuillez cliquer sur le bouton ci-dessous pour réinitialiser votre mot de passe. Ce lien est valable 1 heure.",
		ResetButton:     "Réinitialiser",
	},
	{
		Language:        "it-IT",
		EmailIsAutoSend: "Questa email è inviata automaticamente.",
		ActiveTitle:     "Conferma il tuo account",
		ActiveDes:       "Per favore, clicca sul pulsante qui sotto per confermare il tuo indirizzo email e completare la configurazione del tuo account. Questo link è valido per 24 ore.",
		ActiveButton:    "Conferma",
		ResetTitle:      "Reimposta la tua password",
		ResetDes:        "Per favore, clicca sul pulsante qui sotto per reimpostare la tua password. Questo link è valido per 1 ora.",
		ResetButton:     "Reimposta",
	},
	{
		Language:        "ja-JP",
		EmailIsAutoSend: "このメールはシステムによって自動的に送信されました。",
		ActiveTitle:     "アカウントを確認する",
		ActiveDes:       "アカウントの設定を完了するために、以下のボタンをクリックしてメールアドレスを確認してください。このリンクは24時間有効です。",
		ActiveButton:    "確認する",
		ResetTitle:      "パスワードをリセットする",
		ResetDes:        "以下のボタンをクリックしてパスワードをリセットしてください。このリンクは1時間有効です。",
		ResetButton:     "リセットする",
	},
	{
		Language:        "ko-KR",
		EmailIsAutoSend: "이 이메일은 시스템에 의해 자동으로 전송됩니다.",
		ActiveTitle:     "계정 확인",
		ActiveDes:       "아래 버튼을 클릭하여 이메일 주소를 확인하고 계정을 설정하세요. 이 링크는 24시간 동안 유효합니다.",
		ActiveButton:    "확인",
		ResetTitle:      "비밀번호 재설정",
		ResetDes:        "아래 버튼을 클릭하여 비밀번호를 재설정하세요. 이 링크는 1시간 동안 유효합니다.",
		ResetButton:     "비밀번호 재설정",
	},
	{
		Language:        "pt-BR",
		EmailIsAutoSend: "Este e-mail é enviado automaticamente.",
		ActiveTitle:     "Confirme sua conta",
		ActiveDes:       "Por favor, clique no botão abaixo para confirmar seu endereço de e-mail e concluir a configuração da sua conta. Este link é válido por 24 horas.",
		ActiveButton:    "Confirmar",
		ResetTitle:      "Redefinir sua senha",
		ResetDes:        "Por favor, clique no botão abaixo para redefinir sua senha. Este link é válido por 1 hora.",
		ResetButton:     "Redefinir",
	},
	{
		Language:        "ru-RU",
		EmailIsAutoSend: "Это письмо отправлено автоматически.",
		ActiveTitle:     "Подтвердите вашу учетную запись",
		ActiveDes:       "Пожалуйста, нажмите кнопку ниже, чтобы подтвердить ваш адрес электронной почты и завершить настройку вашей учетной записи. Эта ссылка действительна в течение 24 часов.",
		ActiveButton:    "Подтвердить",
		ResetTitle:      "Сбросить ваш пароль",
		ResetDes:        "Пожалуйста, нажмите кнопку ниже, чтобы сбросить ваш пароль. Эта ссылка действительна в течение 1 часа.",
		ResetButton:     "Сбросить пароль",
	},
}

var DefaultSettings = map[string]string{
	"siteURL":                                 `http://localhost:5212`,
	"siteName":                                `Cloudreve`,
	"siteDes":                                 "Cloudreve",
	"siteID":                                  uuid.Must(uuid.NewV4()).String(),
	"siteTitle":                               "Cloud storage for everyone",
	"siteScript":                              "",
	"pwa_small_icon":                          "/static/img/favicon.ico",
	"pwa_medium_icon":                         "/static/img/logo192.png",
	"pwa_large_icon":                          "/static/img/logo512.png",
	"pwa_display":                             "standalone",
	"pwa_theme_color":                         "#000000",
	"pwa_background_color":                    "#ffffff",
	"register_enabled":                        `1`,
	"default_group":                           `2`,
	"fromName":                                `Cloudreve`,
	"mail_keepalive":                          `30`,
	"fromAdress":                              `no-reply@cloudreve.org`,
	"smtpHost":                                `smtp.cloudreve.com`,
	"smtpPort":                                `25`,
	"replyTo":                                 `support@cloudreve.org`,
	"smtpUser":                                `smtp.cloudreve.com`,
	"smtpPass":                                ``,
	"smtpEncryption":                          `0`,
	"ban_time":                                `604800`,
	"maxEditSize":                             `52428800`,
	"archive_timeout":                         `600`,
	"upload_session_timeout":                  `86400`,
	"slave_api_timeout":                       `60`,
	"folder_props_timeout":                    `300`,
	"chunk_retries":                           `5`,
	"use_temp_chunk_buffer":                   `1`,
	"login_captcha":                           `0`,
	"reg_captcha":                             `0`,
	"email_active":                            `0`,
	"forget_captcha":                          `0`,
	"gravatar_server":                         `https://www.gravatar.com/`,
	"defaultTheme":                            `#1976d2`,
	"theme_options":                           `{"#1976d2":{"light":{"palette":{"primary":{"main":"#1976d2","light":"#42a5f5","dark":"#1565c0"},"secondary":{"main":"#9c27b0","light":"#ba68c8","dark":"#7b1fa2"}}},"dark":{"palette":{"primary":{"main":"#90caf9","light":"#e3f2fd","dark":"#42a5f5"},"secondary":{"main":"#ce93d8","light":"#f3e5f5","dark":"#ab47bc"}}}},"#3f51b5":{"light":{"palette":{"primary":{"main":"#3f51b5"},"secondary":{"main":"#f50057"}}},"dark":{"palette":{"primary":{"main":"#9fa8da"},"secondary":{"main":"#ff4081"}}}}}`,
	"max_parallel_transfer":                   `4`,
	"secret_key":                              util.RandStringRunesCrypto(256),
	"temp_path":                               "temp",
	"avatar_path":                             "avatar",
	"avatar_size":                             "4194304",
	"avatar_size_l":                           "200",
	"cron_garbage_collect":                    "@every 30m",
	"cron_entity_collect":                     "@every 15m",
	"cron_trash_bin_collect":                  "@every 33m",
	"cron_oauth_cred_refresh":                 "@every 230h",
	"authn_enabled":                           "0",
	"captcha_type":                            "normal",
	"captcha_height":                          "60",
	"captcha_width":                           "240",
	"captcha_mode":                            "3",
	"captcha_ComplexOfNoiseText":              "0",
	"captcha_ComplexOfNoiseDot":               "0",
	"captcha_IsShowHollowLine":                "0",
	"captcha_IsShowNoiseDot":                  "1",
	"captcha_IsShowNoiseText":                 "0",
	"captcha_IsShowSlimeLine":                 "1",
	"captcha_IsShowSineLine":                  "0",
	"captcha_CaptchaLen":                      "6",
	"captcha_ReCaptchaKey":                    "defaultKey",
	"captcha_ReCaptchaSecret":                 "defaultSecret",
	"captcha_turnstile_site_key":              "",
	"captcha_turnstile_site_secret":           "",
	"captcha_cap_instance_url":                "",
	"captcha_cap_site_key":                    "",
	"captcha_cap_secret_key":                  "",
	"captcha_cap_asset_server":                "jsdelivr",
	"thumb_width":                             "400",
	"thumb_height":                            "300",
	"thumb_entity_suffix":                     "{blob_path}/{blob_name}._thumb",
	"thumb_slave_sidecar_suffix":              "._thumb_sidecar",
	"thumb_encode_method":                     "png",
	"thumb_gc_after_gen":                      "0",
	"thumb_encode_quality":                    "95",
	"thumb_builtin_enabled":                   "1",
	"thumb_builtin_max_size":                  "78643200", // 75 MB
	"thumb_vips_max_size":                     "78643200", // 75 MB
	"thumb_vips_enabled":                      "0",
	"thumb_vips_exts":                         "3fr,ari,arw,bay,braw,crw,cr2,cr3,cap,data,dcs,dcr,dng,drf,eip,erf,fff,gpr,iiq,k25,kdc,mdc,mef,mos,mrw,nef,nrw,obm,orf,pef,ptx,pxn,r3d,raf,raw,rwl,rw2,rwz,sr2,srf,srw,tif,x3f,csv,mat,img,hdr,pbm,pgm,ppm,pfm,pnm,svg,svgz,j2k,jp2,jpt,j2c,jpc,gif,png,jpg,jpeg,jpe,webp,tif,tiff,fits,fit,fts,exr,jxl,pdf,heic,heif,avif,svs,vms,vmu,ndpi,scn,mrxs,svslide,bif,raw",
	"thumb_ffmpeg_enabled":                    "0",
	"thumb_vips_path":                         "vips",
	"thumb_ffmpeg_path":                       "ffmpeg",
	"thumb_ffmpeg_max_size":                   "10737418240", // 10 GB
	"thumb_ffmpeg_exts":                       "3g2,3gp,asf,asx,avi,divx,flv,m2ts,m2v,m4v,mkv,mov,mp4,mpeg,mpg,mts,mxf,ogv,rm,swf,webm,wmv",
	"thumb_ffmpeg_seek":                       "00:00:01.00",
	"thumb_ffmpeg_extra_args":                 "-hwaccel auto",
	"thumb_libreoffice_path":                  "soffice",
	"thumb_libreoffice_max_size":              "78643200", // 75 MB
	"thumb_libreoffice_enabled":               "0",
	"thumb_libreoffice_exts":                  "txt,pdf,md,ods,ots,fods,uos,xlsx,xml,xls,xlt,dif,dbf,html,slk,csv,xlsm,docx,dotx,doc,dot,rtf,xlsm,xlst,xls,xlw,xlc,xlt,pptx,ppsx,potx,pomx,ppt,pps,ppm,pot,pom",
	"thumb_music_cover_enabled":               "1",
	"thumb_music_cover_exts":                  "mp3,m4a,ogg,flac",
	"thumb_music_cover_max_size":              "1073741824", // 1 GB
	"thumb_libraw_enabled":                    "0",
	"thumb_libraw_path":                       "simple_dcraw",
	"thumb_libraw_max_size":                   "78643200", // 75 MB
	"thumb_libraw_exts":                       "3fr,ari,arw,bay,braw,crw,cr2,cr3,cap,data,dcs,dcr,dng,drf,eip,erf,fff,gpr,iiq,k25,kdc,mdc,mef,mos,mrw,nef,nrw,obm,orf,pef,ptx,pxn,r3d,raf,raw,rwl,rw2,rwz,sr2,srf,srw,tif,x3f",
	"phone_required":                          "false",
	"phone_enabled":                           "false",
	"show_app_promotion":                      "1",
	"public_resource_maxage":                  "86400",
	"viewer_session_timeout":                  "36000",
	"hash_id_salt":                            util.RandStringRunesCrypto(64),
	"access_token_ttl":                        "3600",
	"refresh_token_ttl":                       "1209600", // 2 weeks
	"use_cursor_pagination":                   "1",
	"max_page_size":                           "2000",
	"max_recursive_searched_folder":           "65535",
	"max_batched_file":                        "3000",
	"queue_media_meta_worker_num":             "30",
	"queue_media_meta_max_execution":          "3600",
	"queue_media_meta_backoff_factor":         "2",
	"queue_media_meta_backoff_max_duration":   "60",
	"queue_media_meta_max_retry":              "1",
	"queue_media_meta_retry_delay":            "0",
	"queue_content_processing_worker_num":     "30",
	"queue_content_processing_max_execution":  "3600",
	"queue_content_processing_backoff_factor": "2",
	"queue_content_processing_backoff_max_duration": "60",
	"queue_content_processing_max_retry":            "1",
	"queue_content_processing_retry_delay":          "0",
	"queue_thumb_worker_num":                        "15",
	"queue_thumb_max_execution":                     "300",
	"queue_thumb_backoff_factor":                    "2",
	"queue_thumb_backoff_max_duration":              "60",
	"queue_thumb_max_retry":                         "0",
	"queue_thumb_retry_delay":                       "0",
	"queue_recycle_worker_num":                      "5",
	"queue_recycle_max_execution":                   "900",
	"queue_recycle_backoff_factor":                  "2",
	"queue_recycle_backoff_max_duration":            "60",
	"queue_recycle_max_retry":                       "0",
	"queue_recycle_retry_delay":                     "0",
	"queue_io_intense_worker_num":                   "30",
	"queue_io_intense_max_execution":                "2592000",
	"queue_io_intense_backoff_factor":               "2",
	"queue_io_intense_backoff_max_duration":         "600",
	"queue_io_intense_max_retry":                    "5",
	"queue_io_intense_retry_delay":                  "0",
	"queue_remote_download_worker_num":              "5",
	"queue_remote_download_max_execution":           "864000",
	"queue_remote_download_backoff_factor":          "2",
	"queue_remote_download_backoff_max_duration":    "600",
	"queue_remote_download_max_retry":               "5",
	"queue_remote_download_retry_delay":             "0",
	"entity_url_default_ttl":                        "3600",
	"entity_url_cache_margin":                       "600",
	"media_meta":                                    "1",
	"media_meta_exif":                               "1",
	"media_meta_exif_size_local":                    "1073741824",
	"media_meta_exif_size_remote":                   "104857600",
	"media_meta_exif_brute_force":                   "1",
	"media_meta_music":                              "1",
	"media_meta_music_size_local":                   "1073741824",
	"media_exif_music_size_remote":                  "1073741824",
	"media_meta_ffprobe":                            "0",
	"media_meta_ffprobe_path":                       "ffprobe",
	"media_meta_ffprobe_size_local":                 "0",
	"media_meta_ffprobe_size_remote":                "0",
	"media_meta_geocoding":                          "0",
	"media_meta_geocoding_mapbox_ak":                "",
	"site_logo":                                     "/static/img/logo.svg",
	"site_logo_light":                               "/static/img/logo_light.svg",
	"tos_url":                                       "https://cloudreve.org/privacy-policy",
	"privacy_policy_url":                            "https://cloudreve.org/privacy-policy",
	"explorer_category_image_query":                 "type=file&ext=bmp&ext=iff&ext=png&ext=gif&ext=jpg&ext=jpeg&ext=psd&ext=svg&ext=webp&ext=heif&ext=heic&ext=tiff&ext=avif&ext=3fr&ext=ari&ext=arw&ext=bay&ext=braw&ext=crw&ext=cr2&ext=cr3&ext=cap&ext=dcs&ext=dcr&ext=dng&ext=drf&ext=eip&ext=erf&ext=fff&ext=gpr&ext=iiq&ext=k25&ext=kdc&ext=mdc&ext=mef&ext=mos&ext=mrw&ext=nef&ext=nrw&ext=obm&ext=orf&ext=pef&ext=ptx&ext=pxn&ext=r3d&ext=raf&ext=raw&ext=rwl&ext=rw2&ext=rwz&ext=sr2&ext=srf&ext=srw&ext=tif&ext=x3f",
	"explorer_category_video_query":                 "type=file&ext=mp4&ext=m3u8&ext=flv&ext=avi&ext=wmv&ext=mkv&ext=rm&ext=rmvb&ext=mov&ext=ogv",
	"explorer_category_audio_query":                 "type=file&ext=mp3&ext=flac&ext=ape&ext=wav&ext=acc&ext=ogg&ext=m4a",
	"explorer_category_document_query":              "type=file&ext=pdf&ext=doc&ext=docx&ext=ppt&ext=pptx&ext=xls&ext=xlsx&ext=csv&ext=txt&ext=md&ext=pub",
	"use_sse_for_search":                            "0",
	"emojis":                                        `{"😀":["😀","😃","😄","😁","😆","😅","🤣","😂","🙂","🙃","🫠","😉","😊","😇","🥰","😍","🤩","😘","😗","😚","😙","🥲","😋","😛","😜","🤪","😝","🤑","🤗","🤭","🫢","🫣","🤫","🤔","🫡","🤐","🤨","😐","😑","😶","😶‍🌫️","😏","😒","🙄","😬","😮‍💨","🤥","😌","😔","😪","🤤","😴","😷","🤒","🤕","🤢","🤮","🤧","🥵","🥶","🥴","😵","😵‍💫","🤯","🤠","🥳","🥸","😎","🤓","🧐","😕","🫤","😟","🙁","😮","😯","😲","😳","🥺","🥹","😦","😧","😨","😰","😥","😢","😭","😱","😖","😣","😞","😓","😩","😫","🥱","😤","😡","😠","🤬","😈","👿","💀","☠️","💩","🤡","👹","👺","👻","👽","👾","🤖","😺","😸","😹","😻","😼","😽","🙀","😿","😾","🙈","🙉","🙊","💋","💌","💘","💝","💖","💗","💓","💞","💕","💟","💔","❤️‍🔥","❤️‍🩹","❤️","🧡","💛","💚","💙","💜","🤎","🖤","🤍","💯","💢","💥","💫","💦","💨","🕳️","💣","💬","👁️‍🗨️","🗨️","🗯️","💭","💤"],"👋":["👋","🤚","🖐️","✋","🖖","🫱","🫲","🫳","🫴","👌","🤌","🤏","✌️","🤞","🫰","🤟","🤘","🤙","👈","👉","👆","🖕","👇","☝️","🫵","👍","👎","✊","👊","🤛","🤜","👏","🙌","🫶","👐","🤲","🤝","🙏","✍️","💅","🤳","💪","🦾","🦿","🦵","🦶","👂","🦻","👃","🧠","🫀","🫁","🦷","🦴","👀","👁️","👅","👄","🫦","👶","🧒","👦","👧","🧑","👱","👨","🧔","🧔‍♂️","🧔‍♀️","👨‍🦰","👨‍🦱","👨‍🦳","👨‍🦲","👩","👩‍🦰","🧑‍🦰","👩‍🦱","🧑‍🦱","👩‍🦳","🧑‍🦳","👩‍🦲","🧑‍🦲","👱‍♀️","👱‍♂️","🧓","👴","👵","🙍","🙍‍♂️","🙍‍♀️","🙎","🙎‍♂️","🙎‍♀️","🙅","🙅‍♂️","🙅‍♀️","🙆","🙆‍♂️","🙆‍♀️","💁","💁‍♂️","💁‍♀️","🙋","🙋‍♂️","🙋‍♀️","🧏","🧏‍♂️","🧏‍♀️","🙇","🙇‍♂️","🙇‍♀️","🤦","🤦‍♂️","🤦‍♀️","🤷","🤷‍♂️","🤷‍♀️","🧑‍⚕️","👨‍⚕️","👩‍⚕️","🧑‍🎓","👨‍🎓","👩‍🎓","🧑‍🏫","👨‍🏫","👩‍🏫","🧑‍⚖️","👨‍⚖️","👩‍⚖️","🧑‍🌾","👨‍🌾","👩‍🌾","🧑‍🍳","👨‍🍳","👩‍🍳","🧑‍🔧","👨‍🔧","👩‍🔧","🧑‍🏭","👨‍🏭","👩‍🏭","🧑‍💼","👨‍💼","👩‍💼","🧑‍🔬","👨‍🔬","👩‍🔬","🧑‍💻","👨‍💻","👩‍💻","🧑‍🎤","👨‍🎤","👩‍🎤","🧑‍🎨","👨‍🎨","👩‍🎨","🧑‍✈️","👨‍✈️","👩‍✈️","🧑‍🚀","👨‍🚀","👩‍🚀","🧑‍🚒","👨‍🚒","👩‍🚒","👮","👮‍♂️","👮‍♀️","🕵️","🕵️‍♂️","🕵️‍♀️","💂","💂‍♂️","💂‍♀️","🥷","👷","👷‍♂️","👷‍♀️","🫅","🤴","👸","👳","👳‍♂️","👳‍♀️","👲","🧕","🤵","🤵‍♂️","🤵‍♀️","👰","👰‍♂️","👰‍♀️","🤰","🫃","🫄","🤱","👩‍🍼","👨‍🍼","🧑‍🍼","👼","🎅","🤶","🧑‍🎄","🦸","🦸‍♂️","🦸‍♀️","🦹","🦹‍♂️","🦹‍♀️","🧙","🧙‍♂️","🧙‍♀️","🧚","🧚‍♂️","🧚‍♀️","🧛","🧛‍♂️","🧛‍♀️","🧜","🧜‍♂️","🧜‍♀️","🧝","🧝‍♂️","🧝‍♀️","🧞","🧞‍♂️","🧞‍♀️","🧟","🧟‍♂️","🧟‍♀️","🧌","💆","💆‍♂️","💆‍♀️","💇","💇‍♂️","💇‍♀️","🚶","🚶‍♂️","🚶‍♀️","🧍","🧍‍♂️","🧍‍♀️","🧎","🧎‍♂️","🧎‍♀️","🧑‍🦯","👨‍🦯","👩‍🦯","🧑‍🦼","👨‍🦼","👩‍🦼","🧑‍🦽","👨‍🦽","👩‍🦽","🏃","🏃‍♂️","🏃‍♀️","💃","🕺","🕴️","👯","👯‍♂️","👯‍♀️","🧖","🧖‍♂️","🧖‍♀️","🧗","🧗‍♂️","🧗‍♀️","🤺","🏇","⛷️","🏂","🏌️","🏌️‍♂️","🏌️‍♀️","🏄","🏄‍♂️","🏄‍♀️","🚣","🚣‍♂️","🚣‍♀️","🏊","🏊‍♂️","🏊‍♀️","⛹️","⛹️‍♂️","⛹️‍♀️","🏋️","🏋️‍♂️","🏋️‍♀️","🚴","🚴‍♂️","🚴‍♀️","🚵","🚵‍♂️","🚵‍♀️","🤸","🤸‍♂️","🤸‍♀️","🤼","🤼‍♂️","🤼‍♀️","🤽","🤽‍♂️","🤽‍♀️","🤾","🤾‍♂️","🤾‍♀️","🤹","🤹‍♂️","🤹‍♀️","🧘","🧘‍♂️","🧘‍♀️","🛀","🛌","🧑‍🤝‍🧑","👭","👫","👬","💏","👩‍❤️‍💋‍👨","👨‍❤️‍💋‍👨","👩‍❤️‍💋‍👩","💑","👩‍❤️‍👨","👨‍❤️‍👨","👩‍❤️‍👩","👪","👨‍👩‍👦","👨‍👩‍👧","👨‍👩‍👧‍👦","👨‍👩‍👦‍👦","👨‍👩‍👧‍👧","👨‍👨‍👦","👨‍👨‍👧","👨‍👨‍👧‍👦","👨‍👨‍👦‍👦","👨‍👨‍👧‍👧","👩‍👩‍👦","👩‍👩‍👧","👩‍👩‍👧‍👦","👩‍👩‍👦‍👦","👩‍👩‍👧‍👧","👨‍👦","👨‍👦‍👦","👨‍👧","👨‍👧‍👦","👨‍👧‍👧","👩‍👦","👩‍👦‍👦","👩‍👧","👩‍👧‍👦","👩‍👧‍👧","🗣️","👤","👥","🫂","👣","🦰","🦱","🦳","🦲"],"🐵":["🐵","🐒","🦍","🦧","🐶","🐕","🦮","🐕‍🦺","🐩","🐺","🦊","🦝","🐱","🐈","🐈‍⬛","🦁","🐯","🐅","🐆","🐴","🐎","🦄","🦓","🦌","🦬","🐮","🐂","🐃","🐄","🐷","🐖","🐗","🐽","🐏","🐑","🐐","🐪","🐫","🦙","🦒","🐘","🦣","🦏","🦛","🐭","🐁","🐀","🐹","🐰","🐇","🐿️","🦫","🦔","🦇","🐻","🐻‍❄️","🐨","🐼","🦥","🦦","🦨","🦘","🦡","🐾","🦃","🐔","🐓","🐣","🐤","🐥","🐦","🐧","🕊️","🦅","🦆","🦢","🦉","🦤","🪶","🦩","🦚","🦜","🐸","🐊","🐢","🦎","🐍","🐲","🐉","🦕","🦖","🐳","🐋","🐬","🦭","🐟","🐠","🐡","🦈","🐙","🐚","🪸","🐌","🦋","🐛","🐜","🐝","🪲","🐞","🦗","🪳","🕷️","🕸️","🦂","🦟","🪰","🪱","🦠","💐","🌸","💮","🪷","🏵️","🌹","🥀","🌺","🌻","🌼","🌷","🌱","🪴","🌲","🌳","🌴","🌵","🌾","🌿","☘️","🍀","🍁","🍂","🍃","🪹","🪺"],"🍇":["🍇","🍈","🍉","🍊","🍋","🍌","🍍","🥭","🍎","🍏","🍐","🍑","🍒","🍓","🫐","🥝","🍅","🫒","🥥","🥑","🍆","🥔","🥕","🌽","🌶️","🫑","🥒","🥬","🥦","🧄","🧅","🍄","🥜","🫘","🌰","🍞","🥐","🥖","🫓","🥨","🥯","🥞","🧇","🧀","🍖","🍗","🥩","🥓","🍔","🍟","🍕","🌭","🥪","🌮","🌯","🫔","🥙","🧆","🥚","🍳","🥘","🍲","🫕","🥣","🥗","🍿","🧈","🧂","🥫","🍱","🍘","🍙","🍚","🍛","🍜","🍝","🍠","🍢","🍣","🍤","🍥","🥮","🍡","🥟","🥠","🥡","🦀","🦞","🦐","🦑","🦪","🍦","🍧","🍨","🍩","🍪","🎂","🍰","🧁","🥧","🍫","🍬","🍭","🍮","🍯","🍼","🥛","☕","🫖","🍵","🍶","🍾","🍷","🍸","🍹","🍺","🍻","🥂","🥃","🫗","🥤","🧋","🧃","🧉","🧊","🥢","🍽️","🍴","🥄","🔪","🫙","🏺"],"🌍":["🌍","🌎","🌏","🌐","🗺️","🗾","🧭","🏔️","⛰️","🌋","🗻","🏕️","🏖️","🏜️","🏝️","🏞️","🏟️","🏛️","🏗️","🧱","🪨","🪵","🛖","🏘️","🏚️","🏠","🏡","🏢","🏣","🏤","🏥","🏦","🏨","🏩","🏪","🏫","🏬","🏭","🏯","🏰","💒","🗼","🗽","⛪","🕌","🛕","🕍","⛩️","🕋","⛲","⛺","🌁","🌃","🏙️","🌄","🌅","🌆","🌇","🌉","♨️","🎠","🛝","🎡","🎢","💈","🎪","🚂","🚃","🚄","🚅","🚆","🚇","🚈","🚉","🚊","🚝","🚞","🚋","🚌","🚍","🚎","🚐","🚑","🚒","🚓","🚔","🚕","🚖","🚗","🚘","🚙","🛻","🚚","🚛","🚜","🏎️","🏍️","🛵","🦽","🦼","🛺","🚲","🛴","🛹","🛼","🚏","🛣️","🛤️","🛢️","⛽","🛞","🚨","🚥","🚦","🛑","🚧","⚓","🛟","⛵","🛶","🚤","🛳️","⛴️","🛥️","🚢","✈️","🛩️","🛫","🛬","🪂","💺","🚁","🚟","🚠","🚡","🛰️","🚀","🛸","🛎️","🧳","⌛","⏳","⌚","⏰","⏱️","⏲️","🕰️","🕛","🕧","🕐","🕜","🕑","🕝","🕒","🕞","🕓","🕟","🕔","🕠","🕕","🕡","🕖","🕢","🕗","🕣","🕘","🕤","🕙","🕥","🕚","🕦","🌑","🌒","🌓","🌔","🌕","🌖","🌗","🌘","🌙","🌚","🌛","🌜","🌡️","☀️","🌝","🌞","🪐","⭐","🌟","🌠","🌌","☁️","⛅","⛈️","🌤️","🌥️","🌦️","🌧️","🌨️","🌩️","🌪️","🌫️","🌬️","🌀","🌈","🌂","☂️","☔","⛱️","⚡","❄️","☃️","⛄","☄️","🔥","💧","🌊"],"🎃":["🎃","🎄","🎆","🎇","🧨","✨","🎈","🎉","🎊","🎋","🎍","🎎","🎏","🎐","🎑","🧧","🎀","🎁","🎗️","🎟️","🎫","🎖️","🏆","🏅","🥇","🥈","🥉","⚽","⚾","🥎","🏀","🏐","🏈","🏉","🎾","🥏","🎳","🏏","🏑","🏒","🥍","🏓","🏸","🥊","🥋","🥅","⛳","⛸️","🎣","🤿","🎽","🎿","🛷","🥌","🎯","🪀","🪁","🎱","🔮","🪄","🧿","🪬","🎮","🕹️","🎰","🎲","🧩","🧸","🪅","🪩","🪆","♠️","♥️","♦️","♣️","♟️","🃏","🀄","🎴","🎭","🖼️","🎨","🧵","🪡","🧶","🪢"],"👓":["👓","🕶️","🥽","🥼","🦺","👔","👕","👖","🧣","🧤","🧥","🧦","👗","👘","🥻","🩱","🩲","🩳","👙","👚","👛","👜","👝","🛍️","🎒","🩴","👞","👟","🥾","🥿","👠","👡","🩰","👢","👑","👒","🎩","🎓","🧢","🪖","⛑️","📿","💄","💍","💎","🔇","🔈","🔉","🔊","📢","📣","📯","🔔","🔕","🎼","🎵","🎶","🎙️","🎚️","🎛️","🎤","🎧","📻","🎷","🪗","🎸","🎹","🎺","🎻","🪕","🥁","🪘","📱","📲","☎️","📞","📟","📠","🔋","🪫","🔌","💻","🖥️","🖨️","⌨️","🖱️","🖲️","💽","💾","💿","📀","🧮","🎥","🎞️","📽️","🎬","📺","📷","📸","📹","📼","🔍","🔎","🕯️","💡","🔦","🏮","🪔","📔","📕","📖","📗","📘","📙","📚","📓","📒","📃","📜","📄","📰","🗞️","📑","🔖","🏷️","💰","🪙","💴","💵","💶","💷","💸","💳","🧾","💹","✉️","📧","📨","📩","📤","📥","📦","📫","📪","📬","📭","📮","🗳️","✏️","✒️","🖋️","🖊️","🖌️","🖍️","📝","💼","📁","📂","🗂️","📅","📆","🗒️","🗓️","📇","📈","📉","📊","📋","📌","📍","📎","🖇️","📏","📐","✂️","🗃️","🗄️","🗑️","🔒","🔓","🔏","🔐","🔑","🗝️","🔨","🪓","⛏️","⚒️","🛠️","🗡️","⚔️","🔫","🪃","🏹","🛡️","🪚","🔧","🪛","🔩","⚙️","🗜️","⚖️","🦯","🔗","⛓️","🪝","🧰","🧲","🪜","⚗️","🧪","🧫","🧬","🔬","🔭","📡","💉","🩸","💊","🩹","🩼","🩺","🩻","🚪","🛗","🪞","🪟","🛏️","🛋️","🪑","🚽","🪠","🚿","🛁","🪤","🪒","🧴","🧷","🧹","🧺","🧻","🪣","🧼","🫧","🪥","🧽","🧯","🛒","🚬","⚰️","🪦","⚱️","🗿","🪧","🪪"],"🏧":["🏧","🚮","🚰","♿","🚹","🚺","🚻","🚼","🚾","🛂","🛃","🛄","🛅","⚠️","🚸","⛔","🚫","🚳","🚭","🚯","🚱","🚷","📵","🔞","☢️","☣️","⬆️","↗️","➡️","↘️","⬇️","↙️","⬅️","↖️","↕️","↔️","↩️","↪️","⤴️","⤵️","🔃","🔄","🔙","🔚","🔛","🔜","🔝","🛐","⚛️","🕉️","✡️","☸️","☯️","✝️","☦️","☪️","☮️","🕎","🔯","♈","♉","♊","♋","♌","♍","♎","♏","♐","♑","♒","♓","⛎","🔀","🔁","🔂","▶️","⏩","⏭️","⏯️","◀️","⏪","⏮️","🔼","⏫","🔽","⏬","⏸️","⏹️","⏺️","⏏️","🎦","🔅","🔆","📶","📳","📴","♀️","♂️","⚧️","✖️","➕","➖","➗","🟰","♾️","‼️","⁉️","❓","❔","❕","❗","〰️","💱","💲","⚕️","♻️","⚜️","🔱","📛","🔰","⭕","✅","☑️","✔️","❌","❎","➰","➿","〽️","✳️","✴️","❇️","©️","®️","™️","#️⃣","*️⃣","0️⃣","1️⃣","2️⃣","3️⃣","4️⃣","5️⃣","6️⃣","7️⃣","8️⃣","9️⃣","🔟","🔠","🔡","🔢","🔣","🔤","🅰️","🆎","🅱️","🆑","🆒","🆓","ℹ️","🆔","Ⓜ️","🆕","🆖","🅾️","🆗","🅿️","🆘","🆙","🆚","🈁","🈂️","🈷️","🈶","🈯","🉐","🈹","🈚","🈲","🉑","🈸","🈴","🈳","㊗️","㊙️","🈺","🈵","🔴","🟠","🟡","🟢","🔵","🟣","🟤","⚫","⚪","🟥","🟧","🟨","🟩","🟦","🟪","🟫","⬛","⬜","◼️","◻️","◾","◽","▪️","▫️","🔶","🔷","🔸","🔹","🔺","🔻","💠","🔘","🔳","🔲"],"🏁":["🏁","🚩","🎌","🏴","🏳️","🏳️‍🌈","🏳️‍⚧️","🏴‍☠️","🇦🇨","🇦🇩","🇦🇪","🇦🇫","🇦🇬","🇦🇮","🇦🇱","🇦🇲","🇦🇴","🇦🇶","🇦🇷","🇦🇸","🇦🇹","🇦🇺","🇦🇼","🇦🇽","🇦🇿","🇧🇦","🇧🇧","🇧🇩","🇧🇪","🇧🇫","🇧🇬","🇧🇭","🇧🇮","🇧🇯","🇧🇱","🇧🇲","🇧🇳","🇧🇴","🇧🇶","🇧🇷","🇧🇸","🇧🇹","🇧🇻","🇧🇼","🇧🇾","🇧🇿","🇨🇦","🇨🇨","🇨🇩","🇨🇫","🇨🇬","🇨🇭","🇨🇮","🇨🇰","🇨🇱","🇨🇲","🇨🇳","🇨🇴","🇨🇵","🇨🇷","🇨🇺","🇨🇻","🇨🇼","🇨🇽","🇨🇾","🇨🇿","🇩🇪","🇩🇬","🇩🇯","🇩🇰","🇩🇲","🇩🇴","🇩🇿","🇪🇦","🇪🇨","🇪🇪","🇪🇬","🇪🇭","🇪🇷","🇪🇸","🇪🇹","🇪🇺","🇫🇮","🇫🇯","🇫🇰","🇫🇲","🇫🇴","🇫🇷","🇬🇦","🇬🇧","🇬🇩","🇬🇪","🇬🇫","🇬🇬","🇬🇭","🇬🇮","🇬🇱","🇬🇲","🇬🇳","🇬🇵","🇬🇶","🇬🇷","🇬🇸","🇬🇹","🇬🇺","🇬🇼","🇬🇾","🇭🇰","🇭🇲","🇭🇳","🇭🇷","🇭🇹","🇭🇺","🇮🇨","🇮🇩","🇮🇪","🇮🇱","🇮🇲","🇮🇳","🇮🇴","🇮🇶","🇮🇷","🇮🇸","🇮🇹","🇯🇪","🇯🇲","🇯🇴","🇯🇵","🇰🇪","🇰🇬","🇰🇭","🇰🇮","🇰🇲","🇰🇳","🇰🇵","🇰🇷","🇰🇼","🇰🇾","🇰🇿","🇱🇦","🇱🇧","🇱🇨","🇱🇮","🇱🇰","🇱🇷","🇱🇸","🇱🇹","🇱🇺","🇱🇻","🇱🇾","🇲🇦","🇲🇨","🇲🇩","🇲🇪","🇲🇫","🇲🇬","🇲🇭","🇲🇰","🇲🇱","🇲🇲","🇲🇳","🇲🇴","🇲🇵","🇲🇶","🇲🇷","🇲🇸","🇲🇹","🇲🇺","🇲🇻","🇲🇼","🇲🇽","🇲🇾","🇲🇿","🇳🇦","🇳🇨","🇳🇪","🇳🇫","🇳🇬","🇳🇮","🇳🇱","🇳🇴","🇳🇵","🇳🇷","🇳🇺","🇳🇿","🇴🇲","🇵🇦","🇵🇪","🇵🇫","🇵🇬","🇵🇭","🇵🇰","🇵🇱","🇵🇲","🇵🇳","🇵🇷","🇵🇸","🇵🇹","🇵🇼","🇵🇾","🇶🇦","🇷🇪","🇷🇴","🇷🇸","🇷🇺","🇷🇼","🇸🇦","🇸🇧","🇸🇨","🇸🇩","🇸🇪","🇸🇬","🇸🇭","🇸🇮","🇸🇯","🇸🇰","🇸🇱","🇸🇲","🇸🇳","🇸🇴","🇸🇷","🇸🇸","🇸🇹","🇸🇻","🇸🇽","🇸🇾","🇸🇿","🇹🇦","🇹🇨","🇹🇩","🇹🇫","🇹🇬","🇹🇭","🇹🇯","🇹🇰","🇹🇱","🇹🇲","🇹🇳","🇹🇴","🇹🇷","🇹🇹","🇹🇻","🇹🇼","🇹🇿","🇺🇦","🇺🇬","🇺🇲","🇺🇳","🇺🇸","🇺🇾","🇺🇿","🇻🇦","🇻🇨","🇻🇪","🇻🇬","🇻🇮","🇻🇳","🇻🇺","🇼🇫","🇼🇸","🇽🇰","🇾🇪","🇾🇹","🇿🇦","🇿🇲","🇿🇼","🏴󠁧󠁢󠁥󠁮󠁧󠁿","🏴󠁧󠁢󠁳󠁣󠁴󠁿","🏴󠁧󠁢󠁷󠁬󠁳󠁿"]}`,
	"map_provider":                                  "openstreetmap",
	"map_google_tile_type":                          "regular",
	"map_mapbox_ak":                                 "",
	"mime_mapping":                                  defaultMimeMapping,
	"logto_enabled":                                 "0",
	"logto_config":                                  `{"direct_sign_in":true,"display_name":"vas.sso"}`,
	"qq_login":                                      `0`,
	"qq_login_config":                               `{"direct_sign_in":false}`,
	"license":                                       "",
	"custom_nav_items":                              "[]",
	"oidc_enabled":                                  "0",
	"oidc_display_name":                             "统一认证",
	"oidc_auto_redirect":                            "0",
	"oidc_config_mode":                              "remote",
	"oidc_binding_code":                             "cloudreve-main",
	"oidc_sso_url":                                  "",
	"oidc_wellknown_url":                            "",
	"oidc_client_id":                                "",
	"oidc_client_secret":                            "",
	"oidc_scope":                                    "openid profile email user_info user.read UserInfo.Read Admin.Read Files.Read Files.Write Workflow.Read Workflow.Write Shares.Read Shares.Write",
	"headless_footer_html":                          "",
	"headless_bottom_html":                          "",
	"sidebar_bottom_html":                           "",
	"encrypt_master_key":                            "",
	"encrypt_master_key_vault":                      "setting",
	"encrypt_master_key_file":                       "",
	"show_encryption_status":                        "1",
	"show_desktop_app_promotion":                    "1",
	"syncthing_upgrade_version":                     "",
	"syncthing_download_linux_url":                  "https://syncthing.net/downloads/#linux",
	"syncthing_download_windows_url":                "https://syncthing.net/downloads/#windows",
	"fs_event_push_enabled":                         "1",
	"fs_event_push_max_age":                         "1209600",
	"fs_event_push_debounce":                        "5",
	"audit_log_enabled_types":                       defaultAuditLogEnabledTypes,
	"fts_enabled":                                   "0",
	"fts_sync_folders":                              "0",
	"fts_index_type":                                "elasticsearch",
	"fts_extractor_type":                            "tika",
	"fts_meilisearch_endpoint":                      "",
	"fts_meilisearch_api_key":                       "",
	"fts_meilisearch_page_size":                     "5",
	"fts_meilisearch_embed_enabled":                 "0",
	"fts_meilisearch_embed_config":                  "{}",
	"fts_elasticsearch_endpoint":                    "",
	"fts_elasticsearch_cloud_id":                    "",
	"fts_elasticsearch_api_key":                     "",
	"fts_elasticsearch_username":                    "",
	"fts_elasticsearch_password":                    "",
	"fts_elasticsearch_index":                       "cloudreve_files",
	"fts_elasticsearch_page_size":                   "5",
	"fts_elasticsearch_skip_tls_verify":             "0",
	"fts_tika_endpoint":                             "",
	"fts_tika_document_enabled":                     "1",
	"fts_tika_document_exts":                        "pdf,txt,text,md,markdown,csv,tsv,html,htm,xhtml,xml,rtf,epub,fb2,chm,mif,doc,dot,docx,docm,dotx,dotm,wps,wks,wri,hwp,one,wpd,xls,xlt,xla,xlc,xlm,xlw,xlsx,xlsm,xltx,xltm,xlsb,xlam,qpw,ppt,pps,pot,pptx,pptm,ppsx,ppsm,potx,potm,sldx,sldm,ppam,vsd,vst,vss,vsdx,vstx,vssx,vsdm,vstm,vssm,pub,mpp,xps,dwfx,odt,fodt,ott,odm,oth,ods,fods,ots,odp,fodp,otp,odg,fodg,otg,odc,odf,odb,odi,sxw,stw,sxg,sxc,stc,sxi,sti,sxd,std,sxm,pages,numbers,key,eml,mht,mhtml,nws,msg,pst,mbox,tnef",
	"fts_tika_archive_enabled":                      "1",
	"fts_tika_archive_exts":                         "zip,tar,tgz,tbz,tbz2,txz,tlz,7z,rar,ar,gz,z,bz,bz2,xz,lzma,lz4,br,snappy,sz,pack200,cpio,arj,dump,jar,war,ear",
	"fts_tika_max_file_size":                        "26214400",
	"fts_tika_sidecar_enabled":                      "0",
	"fts_tika_sidecar_text_enabled":                 "1",
	"fts_tika_sidecar_assets_enabled":               "1",
	"fts_tika_extract_inline_images":                "0",
	"fts_external_enabled":                          "0",
	"fts_external_mode":                             "fallback_on_error_or_quality",
	"fts_external_use_global_kafka":                 "1",
	"fts_external_kafka_brokers":                    "",
	"fts_external_kafka_security_protocol":          "PLAINTEXT",
	"fts_external_kafka_sasl_mechanism":             "PLAIN",
	"fts_external_kafka_username":                   "",
	"fts_external_kafka_password":                   "",
	"fts_external_kafka_tls_skip_verify":            "0",
	"fts_external_kafka_process_topic":              "process",
	"fts_external_kafka_result_topic":               "result",
	"fts_external_kafka_error_topic":                "error",
	"fts_external_kafka_consumer_group":             "cloudreve-fts-external",
	"fts_external_max_file_size_mb":                 "25",
	"fts_external_timeout_seconds":                  "300",
	"fts_external_retry_max":                        "2",
	"fts_external_quality_enabled":                  "1",
	"fts_external_quality_min_text_length":          "32",
	"fts_external_quality_max_replacement_ratio":    "0.02",
	"fts_external_quality_max_control_char_ratio":   "0.01",
	"fts_external_quality_min_printable_ratio":      "0.85",
	"fts_external_quality_font_box_min_count":       "4",
	"fts_external_quality_font_box_min_run":         "3",
	"fts_external_quality_font_box_min_ratio":       "0.35",
	"fts_external_recursive_attachments":            "1",
	"fts_external_ocr_enabled":                      "0",
	"fts_external_skip_encrypted_files":             "1",
	"fts_chunk_size":                                "2000",
	"viewer_default_apps":                           "{}",
}

var RedactedSettings = map[string]struct{}{
	"encrypt_master_key":          {},
	"secret_key":                  {},
	"fts_external_kafka_password": {},
}

func init() {
	explorerIcons, err := json.Marshal(defaultIcons)
	if err != nil {
		panic(err)
	}
	DefaultSettings["explorer_icons"] = string(explorerIcons)

	viewers, err := json.Marshal(defaultFileViewers)
	if err != nil {
		panic(err)
	}
	DefaultSettings["file_viewers"] = string(viewers)

	customProps, err := json.Marshal(defaultFileProps)
	if err != nil {
		panic(err)
	}
	DefaultSettings["custom_props"] = string(customProps)

	activeMails := []map[string]string{}
	for _, langContents := range mailTemplateContents {
		activeMails = append(activeMails, map[string]string{
			"language": langContents.Language,
			"title":    "[{{ .CommonContext.SiteBasic.Name }}] " + langContents.ActiveTitle,
			"body": util.Replace(map[string]string{
				"[[ .Language ]]":        langContents.Language,
				"[[ .ActiveTitle ]]":     langContents.ActiveTitle,
				"[[ .ActiveDes ]]":       langContents.ActiveDes,
				"[[ .ActiveButton ]]":    langContents.ActiveButton,
				"[[ .EmailIsAutoSend ]]": langContents.EmailIsAutoSend,
			}, defaultActiveMailBody),
		})
	}
	mailActivationTemplates, err := json.Marshal(activeMails)
	if err != nil {
		panic(err)
	}
	DefaultSettings["mail_activation_template"] = string(mailActivationTemplates)

	resetMails := []map[string]string{}
	for _, langContents := range mailTemplateContents {
		resetMails = append(resetMails, map[string]string{
			"language": langContents.Language,
			"title":    "[{{ .CommonContext.SiteBasic.Name }}] " + langContents.ResetTitle,
			"body": util.Replace(map[string]string{
				"[[ .Language ]]":        langContents.Language,
				"[[ .ResetTitle ]]":      langContents.ResetTitle,
				"[[ .ResetDes ]]":        langContents.ResetDes,
				"[[ .ResetButton ]]":     langContents.ResetButton,
				"[[ .EmailIsAutoSend ]]": langContents.EmailIsAutoSend,
			}, defaultResetMailBody),
		})
	}
	mailResetTemplates, err := json.Marshal(resetMails)
	if err != nil {
		panic(err)
	}
	DefaultSettings["mail_reset_template"] = string(mailResetTemplates)

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		panic(err)
	}
	DefaultSettings["encrypt_master_key"] = base64.StdEncoding.EncodeToString(key)
}
