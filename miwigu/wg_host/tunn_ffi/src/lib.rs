use std::ffi::{c_char, CStr};
use std::net::IpAddr;
use std::ptr;
use std::slice;

use base64::engine::general_purpose::STANDARD;
use base64::Engine;
use boringtun::noise::{Tunn, TunnResult};
use boringtun::x25519::{PublicKey, StaticSecret};

const DONE: u32 = 0;
const WRITE_TO_NETWORK: u32 = 1;
const ERROR: u32 = 2;
const WRITE_TO_TUNNEL_IPV4: u32 = 4;
const WRITE_TO_TUNNEL_IPV6: u32 = 6;

#[repr(C)]
pub struct WgResult {
    pub op: u32,
    pub size: u64,
}

fn parse_key(text: &str) -> Option<[u8; 32]> {
    let bytes = STANDARD.decode(text.trim()).ok()?;
    bytes.try_into().ok()
}

/// Write the base64 public key for a base64 private key. Returns 0, or -1.
#[no_mangle]
pub unsafe extern "C" fn wireguard_public_key(
    private_b64: *const c_char,
    out: *mut u8,
    out_len: u32,
) -> i32 {
    if private_b64.is_null() || out.is_null() || out_len < 45 {
        return -1;
    }
    let text = match CStr::from_ptr(private_b64).to_str() {
        Ok(text) => text,
        Err(_) => return -1,
    };
    let key = match parse_key(text) {
        Some(key) => key,
        None => return -1,
    };
    let public = PublicKey::from(&StaticSecret::from(key));
    let encoded = STANDARD.encode(public.to_bytes());
    let bytes = encoded.as_bytes();
    if bytes.len() + 1 > out_len as usize {
        return -1;
    }
    ptr::copy_nonoverlapping(bytes.as_ptr(), out, bytes.len());
    *out.add(bytes.len()) = 0;
    0
}

/// Own private key and the peer's public key, both standard base64.
/// Returns null if either key is invalid.
#[no_mangle]
pub unsafe extern "C" fn new_tunnel(
    static_private: *const c_char,
    peer_public: *const c_char,
    keep_alive: u16,
) -> *mut Tunn {
    if static_private.is_null() || peer_public.is_null() {
        return ptr::null_mut();
    }
    let private = match CStr::from_ptr(static_private).to_str() {
        Ok(text) => match parse_key(text) {
            Some(key) => StaticSecret::from(key),
            None => return ptr::null_mut(),
        },
        Err(_) => return ptr::null_mut(),
    };
    let public = match CStr::from_ptr(peer_public).to_str() {
        Ok(text) => match parse_key(text) {
            Some(key) => PublicKey::from(key),
            None => return ptr::null_mut(),
        },
        Err(_) => return ptr::null_mut(),
    };
    let keep_alive = if keep_alive == 0 {
        None
    } else {
        Some(keep_alive)
    };
    let tunn = Tunn::new(private, public, None, keep_alive, 0, None);
    Box::into_raw(Box::new(tunn))
}

#[no_mangle]
pub unsafe extern "C" fn tunnel_free(tunnel: *mut Tunn) {
    if !tunnel.is_null() {
        drop(Box::from_raw(tunnel));
    }
}

fn result_of(res: TunnResult<'_>) -> WgResult {
    match res {
        TunnResult::Done => WgResult { op: DONE, size: 0 },
        TunnResult::Err(_) => WgResult { op: ERROR, size: 0 },
        TunnResult::WriteToNetwork(buf) => WgResult {
            op: WRITE_TO_NETWORK,
            size: buf.len() as u64,
        },
        TunnResult::WriteToTunnelV4(buf, _) => WgResult {
            op: WRITE_TO_TUNNEL_IPV4,
            size: buf.len() as u64,
        },
        TunnResult::WriteToTunnelV6(buf, _) => WgResult {
            op: WRITE_TO_TUNNEL_IPV6,
            size: buf.len() as u64,
        },
    }
}

/// Decapsulate one datagram. A zero src_size continues the previous packet.
/// Output is written into dst.
#[no_mangle]
pub unsafe extern "C" fn wireguard_read(
    tunnel: *mut Tunn,
    src: *const u8,
    src_size: u32,
    dst: *mut u8,
    dst_size: u32,
) -> WgResult {
    let tunnel = &mut *tunnel;
    let src = if src_size == 0 {
        &[][..]
    } else {
        slice::from_raw_parts(src, src_size as usize)
    };
    let dst = slice::from_raw_parts_mut(dst, dst_size as usize);
    result_of(tunnel.decapsulate(None::<IpAddr>, src, dst))
}

/// Encapsulate one IP packet from the tunnel side.
#[no_mangle]
pub unsafe extern "C" fn wireguard_write(
    tunnel: *mut Tunn,
    src: *const u8,
    src_size: u32,
    dst: *mut u8,
    dst_size: u32,
) -> WgResult {
    let tunnel = &mut *tunnel;
    let src = slice::from_raw_parts(src, src_size as usize);
    let dst = slice::from_raw_parts_mut(dst, dst_size as usize);
    result_of(tunnel.encapsulate(src, dst))
}

/// Run handshake and keepalive timers. Call about every 100ms.
#[no_mangle]
pub unsafe extern "C" fn wireguard_tick(
    tunnel: *mut Tunn,
    dst: *mut u8,
    dst_size: u32,
) -> WgResult {
    let tunnel = &mut *tunnel;
    let dst = slice::from_raw_parts_mut(dst, dst_size as usize);
    result_of(tunnel.update_timers(dst))
}

/// Seconds since the last completed handshake, or -1.
#[no_mangle]
pub unsafe extern "C" fn wireguard_handshake_age(tunnel: *mut Tunn) -> i64 {
    let tunnel = &*tunnel;
    match tunnel.stats().0 {
        Some(age) => age.as_secs() as i64,
        None => -1,
    }
}
