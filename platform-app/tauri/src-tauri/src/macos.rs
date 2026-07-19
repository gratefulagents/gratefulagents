// macOS-specific window setup.
//
// Keep this intentionally minimal while the desktop shell stabilizes. The app
// should behave like a normal foreground app before we add decorative effects.

use tauri::{Runtime, WebviewWindow};

pub fn configure_window<R: Runtime>(window: &WebviewWindow<R>) {
    let _ = window.show();
    let _ = window.unminimize();
    let _ = window.set_focus();

    // Let the webview render under the traffic lights.
    let _ = window.set_decorations(true);

    // Apply translucent vibrancy backing so the CSS `--color-sidebar` with
    // alpha composites over the native material. Failure is non-fatal.
    #[cfg(target_os = "macos")]
    {
        use window_vibrancy::{apply_vibrancy, NSVisualEffectMaterial, NSVisualEffectState};
        if let Err(err) = apply_vibrancy(
            window,
            NSVisualEffectMaterial::HudWindow,
            Some(NSVisualEffectState::Active),
            None,
        ) {
            eprintln!("window-vibrancy: {err}");
        }
    }

    disable_keyboard_autocorrect(window);
}

fn disable_keyboard_autocorrect<R: Runtime>(window: &WebviewWindow<R>) {
    if let Err(err) = window.with_webview(|webview| {
        let view = webview.inner().cast();

        unsafe {
            use objc2::sel;
            use objc2_app_kit::NSTextInputTraitType;

            for selector in [
                sel!(setAutocorrectionType:),
                sel!(setSpellCheckingType:),
                sel!(setGrammarCheckingType:),
                sel!(setSmartQuotesType:),
                sel!(setSmartDashesType:),
                sel!(setTextReplacementType:),
                sel!(setTextCompletionType:),
                sel!(setInlinePredictionType:),
            ] {
                set_text_input_trait_if_supported(view, selector, NSTextInputTraitType::No);
            }

            for selector in [
                sel!(setContinuousSpellCheckingEnabled:),
                sel!(setGrammarCheckingEnabled:),
                sel!(setAutomaticSpellingCorrectionEnabled:),
                sel!(setAutomaticTextReplacementEnabled:),
                sel!(setAutomaticQuoteSubstitutionEnabled:),
                sel!(setAutomaticDashSubstitutionEnabled:),
            ] {
                set_bool_if_supported(view, selector, objc2::runtime::Bool::NO);
            }
        }
    }) {
        log::warn!("failed to disable macOS keyboard autocorrect: {err}");
    }
}

unsafe fn set_text_input_trait_if_supported(
    object: *mut objc2::runtime::AnyObject,
    selector: objc2::runtime::Sel,
    value: objc2_app_kit::NSTextInputTraitType,
) {
    use objc2::runtime::MessageReceiver;

    let Some(object) = (unsafe { object.as_ref() }) else {
        return;
    };

    if unsafe { objc2::msg_send![object, respondsToSelector: selector] } {
        let _: () = unsafe { object.send_message(selector, (value,)) };
    }
}

unsafe fn set_bool_if_supported(
    object: *mut objc2::runtime::AnyObject,
    selector: objc2::runtime::Sel,
    value: objc2::runtime::Bool,
) {
    use objc2::runtime::MessageReceiver;

    let Some(object) = (unsafe { object.as_ref() }) else {
        return;
    };

    if unsafe { objc2::msg_send![object, respondsToSelector: selector] } {
        let _: () = unsafe { object.send_message(selector, (value,)) };
    }
}
