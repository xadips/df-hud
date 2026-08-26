# Development

Rust 1.91 or later (`Cargo.toml` `rust-version`). On Linux you also need
Wayland headers (`libwayland-dev`). Packaging is on [Install](install.md#build-from-source).

```sh
make            # cargo build --release, copy to ./df-hud
make check      # cargo test --locked
```

Do not run the tree binary while an installed unit is using it. `make install`
copies to `~/.local/bin` so a rebuild does not leave the running process on a
deleted inode.

## Before you send a change

GitHub Actions runs this list on `main` and on pull requests. Run it locally
first so you are not waiting on CI to tell you fmt failed:

```sh
cargo fmt --check
cargo clippy --locked --target x86_64-unknown-linux-gnu
cargo clippy --locked --target x86_64-pc-windows-gnu
cargo test --locked --target x86_64-unknown-linux-gnu
cargo deny check
```

Windows tests are compiled here, not executed:

```sh
cargo test --locked --no-run --target x86_64-pc-windows-gnu
```

Stay close to how the neighbouring files already look, and use the lockfile
that is in the repo.

This is a small overlay. If you want a new framework, another runtime, or to
split the crate, there should be a reason that is about this program.

## LLMs

Using an LLM to draft a patch is fine. You're the one sending it, so read it,
know what it does, and be ready to change it. Don't paste a generated dump
you haven't gone through.
