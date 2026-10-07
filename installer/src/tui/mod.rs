//! The installer and the menu in the terminal (ratatui).

mod menu;
mod widgets;
mod wizard;

use std::io::IsTerminal;
use std::path::Path;
use std::time::{Duration, Instant};

use anyhow::{Result, bail};
use ratatui::backend::Backend;
use ratatui::buffer::Cell;
use ratatui::crossterm::event::{self, DisableBracketedPaste, EnableBracketedPaste, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::crossterm::style::{Attribute, ResetColor, SetAttribute};
use ratatui::crossterm::terminal::{BeginSynchronizedUpdate, EndSynchronizedUpdate};
use ratatui::style::Modifier;
use ratatui::{DefaultTerminal, Frame, Terminal};

use crate::setup::Options;

pub fn interactive() -> bool {
    std::io::stdin().is_terminal() && std::io::stdout().is_terminal()
}

/// `mikan` without a command: the installer on a fresh server, the menu on an installed one.
pub fn start() -> Result<()> {
    let installed = Path::new(crate::DIR).join(".env").exists();
    if !interactive() {
        if installed {
            bail!("the menu needs a terminal; the commands are in mikan --help");
        }
        bail!("the installer needs a terminal; for scripts: mikan install --yes (see mikan install --help)");
    }
    if crate::setup::unfinished_install() {
        // The menu of a half-installed server has nothing to show: the install goes on.
        return crate::setup::install(Options::default());
    }
    if installed { menu() } else { wizard(Options::default()) }
}

pub fn wizard(opts: Options) -> Result<()> {
    let mut w = wizard::Wizard::new(opts);
    run(&mut w)?;
    if let Some(text) = w.farewell() {
        println!("{text}");
    }
    w.result()
}

pub fn menu() -> Result<()> {
    let mut m = menu::Menu::new()?;
    run(&mut m)?;
    if let Some(text) = m.farewell() {
        println!("{text}");
    }
    Ok(())
}

/// A screen the loop drives.
pub trait Screen {
    fn draw(&mut self, f: &mut Frame);
    /// Takes a key; true ends the screen.
    fn key(&mut self, k: KeyEvent) -> bool;
    /// Picks up background work; runs before every frame.
    fn tick(&mut self);
    /// Takes pasted text (a terminal in bracketed paste mode sends it whole, not as keys).
    fn paste(&mut self, _text: &str) {}
}

fn run(s: &mut dyn Screen) -> Result<()> {
    let mut t = ratatui::init();
    // Without it a pasted line break reads as Enter.
    let _ = ratatui::crossterm::execute!(std::io::stdout(), EnableBracketedPaste);
    let r = drive(&mut t, s);
    let _ = ratatui::crossterm::execute!(std::io::stdout(), DisableBracketedPaste);
    ratatui::restore();
    r
}

/// How often the whole screen is written again while nothing happens.
const REPAINT: Duration = Duration::from_secs(2);

fn drive(t: &mut DefaultTerminal, s: &mut dyn Screen) -> Result<()> {
    let mut painted = Instant::now();
    let mut repaint = false;
    loop {
        s.tick();
        if repaint || painted.elapsed() >= REPAINT {
            // Whatever another program left in the colors goes first; a terminal that knows
            // synchronized output shows the frame at once.
            ratatui::crossterm::queue!(t.backend_mut(), BeginSynchronizedUpdate, ResetColor, SetAttribute(Attribute::Reset))?;
            invalidate(t);
            t.draw(|f| s.draw(f))?;
            ratatui::crossterm::execute!(t.backend_mut(), EndSynchronizedUpdate)?;
            painted = Instant::now();
            repaint = false;
        } else {
            t.draw(|f| s.draw(f))?;
        }
        if event::poll(Duration::from_millis(80))? {
            match event::read()? {
                Event::Key(k) if k.kind == KeyEventKind::Press => {
                    repaint = true;
                    let ctrl_l = k.code == KeyCode::Char('l') && k.modifiers.contains(KeyModifiers::CONTROL);
                    if !ctrl_l && s.key(k) {
                        return Ok(());
                    }
                }
                Event::Paste(text) => {
                    repaint = true;
                    s.paste(&text);
                }
                Event::Resize(..) => repaint = true,
                _ => {}
            }
        }
    }
}

/// Makes the next draw write every cell of the screen. ratatui writes only the cells that
/// changed since its last frame, so what another program prints into the same terminal
/// (`ssh -v` prints its debug lines over the session) would stay for good. The last frame
/// is replaced by one no screen ever draws: every cell differs from it, and nothing is
/// cleared first, so the screen does not flicker.
fn invalidate<B: Backend>(t: &mut Terminal<B>) {
    let mut stale = Cell::new(" ");
    stale.modifier = Modifier::HIDDEN | Modifier::CROSSED_OUT | Modifier::RAPID_BLINK;
    t.current_buffer_mut().content.fill(stale);
    // The filled buffer becomes the last frame, the other one is emptied for the next.
    t.swap_buffers();
}

#[cfg(test)]
mod tests {
    use ratatui::backend::TestBackend;
    use ratatui::widgets::Paragraph;

    use super::*;

    fn frame(f: &mut Frame) {
        f.render_widget(Paragraph::new("mikan"), f.area());
    }

    // Another program writes over the screen: a plain draw leaves its text, the one after
    // invalidate writes every cell again.
    #[test]
    fn a_repaint_overwrites_what_another_program_wrote() {
        let mut t = Terminal::new(TestBackend::new(12, 2)).unwrap();
        t.draw(frame).unwrap();
        let junk = Cell::new("x");
        t.backend_mut().draw((0..12).flat_map(|x| [(x, 0, &junk), (x, 1, &junk)])).unwrap();
        t.draw(frame).unwrap();
        t.backend().assert_buffer_lines(["xxxxxxxxxxxx", "xxxxxxxxxxxx"]);
        invalidate(&mut t);
        t.draw(frame).unwrap();
        t.backend().assert_buffer_lines(["mikan       ", "            "]);
        // and the frames after it are diffs again
        t.backend_mut().draw([(11, 1, &junk)].into_iter()).unwrap();
        t.draw(frame).unwrap();
        t.backend().assert_buffer_lines(["mikan       ", "           x"]);
    }
}
