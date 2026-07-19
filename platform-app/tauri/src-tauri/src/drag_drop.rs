// Window drag-drop handler. Re-emits the native drag-drop event as
// `window://files-dropped` with an array of string paths so the frontend can
// attach files to the active run without needing the filesystem plugin.

use tauri::{DragDropEvent, Emitter, Window, WindowEvent};

pub fn on_window_event(window: &Window, event: &WindowEvent) {
    let WindowEvent::DragDrop(drag) = event else {
        return;
    };
    match drag {
        DragDropEvent::Drop { paths, .. } => {
            let strs: Vec<String> = paths
                .iter()
                .map(|p| p.to_string_lossy().to_string())
                .collect();
            let _ = window.emit("window://files-dropped", strs);
        }
        DragDropEvent::Enter { .. } => {
            let _ = window.emit("window://drag-enter", ());
        }
        DragDropEvent::Leave => {
            let _ = window.emit("window://drag-leave", ());
        }
        _ => {}
    }
}
