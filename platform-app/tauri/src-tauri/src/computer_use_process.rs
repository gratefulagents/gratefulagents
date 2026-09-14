#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct ProcessIdentity {
    pid: u64,
    start_seconds: u64,
    start_microseconds: u64,
}

impl ProcessIdentity {
    pub fn read(pid: u32) -> Result<Self, String> {
        let pid = i32::try_from(pid)
            .ok()
            .filter(|pid| *pid > 0)
            .ok_or("Cannot verify selected process identity")?;
        unsafe extern "C" {
            fn ga_process_identity_read(pid: i32, identity: *mut ProcessIdentity) -> bool;
        }
        let mut identity = Self::default();
        if !unsafe { ga_process_identity_read(pid, &mut identity) } {
            return Err("Cannot verify selected process identity".into());
        }
        Ok(identity)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn malformed_pids_are_rejected() {
        for pid in [0, i32::MAX as u32 + 1, u32::MAX] {
            assert!(ProcessIdentity::read(pid).is_err());
        }
    }

    #[test]
    fn current_process_identity_is_stable_and_exact() {
        let identity = ProcessIdentity::read(std::process::id()).unwrap();
        assert_eq!(identity.pid, u64::from(std::process::id()));
        assert!(identity.start_seconds > 0);
        assert!(identity.start_microseconds < 1000000);
        assert_eq!(identity, ProcessIdentity::read(std::process::id()).unwrap());
        let mut changed = identity;
        changed.start_microseconds += 1;
        assert_ne!(identity, changed);
    }

    #[test]
    fn disappeared_process_is_rejected() {
        let mut child = std::process::Command::new("/bin/sleep")
            .arg("60")
            .spawn()
            .unwrap();
        let identity = ProcessIdentity::read(child.id());
        child.kill().unwrap();
        child.wait().unwrap();
        let identity = identity.unwrap();
        assert_ne!(ProcessIdentity::read(child.id()).ok(), Some(identity));
    }
}
