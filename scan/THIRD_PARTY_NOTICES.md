# Research and third-party notices

The scanner implementation in this directory is original Go code. The following public projects were reviewed to understand protocol architecture and to avoid unsafe product assumptions:

- [IceDynamix/reliquary-archiver](https://github.com/IceDynamix/reliquary-archiver) and [IceDynamix/reliquary](https://github.com/IceDynamix/reliquary), MIT License: reference architecture for separating packet collection, command decoding, export workers and UI state.
- [yixuan-rs/yixuan-rs](https://github.com/yixuan-rs/yixuan-rs), AGPL-3.0: public historical protocol facts such as the existence and shape of `GetEquipDataScRsp`; no source code or generated protocol files are copied into this project.
- [Yidhari-ZS/Yidhari-ZS](https://github.com/Yidhari-ZS/Yidhari-ZS), AGPL-3.0: public historical protocol facts about equipment records and active session key negotiation; no source code is copied into this project.
- [Simplxss/ZZZ-ClientSimulator](https://github.com/Simplxss/ZZZ-ClientSimulator): reviewed only to corroborate historical protocol behavior. The repository had no discoverable license at review time, so none of its code is copied or distributed here.

Historical command ids and field numbers are deliberately not shipped as a usable adapter. They are version-specific and were published for old/patched clients. A real adapter must be independently verified against the exact official client version and region before `Verified` can be true.
