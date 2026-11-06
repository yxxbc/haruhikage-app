package app

import (
	"github.com/egoist/mygo"
	"os"
)

func (a *app) exportLibrary() {
	if a.lib == nil {
		return
	}
	parent := a.win
	a.run(func() {
		path, e := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: parent, Title: "导出本地音乐资料库", DefaultPath: "music-library.json", Filters: []mygo.FileFilter{{Name: "JSON 备份", Extensions: []string{"json"}}}})
		if e != nil || path == "" {
			return
		}
		tmp := path + ".tmp"
		f, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e == nil {
			e = a.lib.Export(a.ctx, f)
			if e == nil {
				e = f.Sync()
			}
			if ce := f.Close(); e == nil {
				e = ce
			}
			if e == nil {
				e = os.Rename(tmp, path)
			}
		}
		if e != nil {
			_ = os.Remove(tmp)
		}
		a.post(func() {
			if e != nil {
				a.toast = "资料库导出失败"
			} else {
				a.toast = "资料库已导出"
			}
		})
	})
}
func (a *app) importLibrary() {
	if a.lib == nil {
		return
	}
	parent := a.win
	a.run(func() {
		paths, e := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent, Title: "导入资料库备份", Filters: []mygo.FileFilter{{Name: "JSON 备份", Extensions: []string{"json"}}}})
		if e != nil || len(paths) == 0 {
			return
		}
		result, e := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Title: "导入备份", Message: "将备份中的本地音乐资料导入当前资料库。损坏文件不会覆盖现有数据。", Buttons: []string{"取消", "导入"}, CancelButton: 0})
		if e != nil || result.Button != 1 {
			return
		}
		f, e := os.Open(paths[0])
		if e == nil {
			e = a.lib.Import(a.ctx, f)
			_ = f.Close()
		}
		a.post(func() {
			if e != nil {
				a.toast = "备份导入失败，现有资料库已保留"
			} else {
				a.toast = "备份已导入"
				a.refreshLibrary()
				a.load(a.route)
			}
		})
	})
}
