# CNPRODWin3.0.0 verification status

This profile describes the official CN Windows 3.0.0 client. It is not a live equipment adapter yet.

Verified on the user-provided installation:

- launcher game version: `3.0.0`;
- client version marker: `CNPRODWin3.0.0`;
- channel/sub-channel: `1 / 2`;
- official region discovery: `prod_gf_cn` / `新艾利都`;
- official QR bootstrap endpoint;
- gateway accepts RSA version `3`.

The following remain deliberately disabled:

- gateway payload decryption and signature verification;
- active game-session authentication;
- 3.0.0 `GetEquipData` command/field layout;
- 3.0.0 equipment and property catalogs.

The official client stores protected metadata and the available public protocol repositories target older or patched clients. This project will not extract protected metadata, inject into the game, read game memory, or copy unlicensed client-key material. `liveScanEnabled` must stay `false` until a lawful, independently verifiable session adapter and complete catalog are available.
