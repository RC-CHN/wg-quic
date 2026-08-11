#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    wg_quic_desktop_lib::run();
}
