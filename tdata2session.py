#!/usr/bin/env python3
"""
tdata2session — convert Telegram Desktop tdata to tg-cli session.json

Usage:
    python tdata2session.py <tdata_path> [--out ~/.tg-cli/sessions]

Requirements:
    pip install opentele
"""
import argparse
import asyncio
import base64
import hashlib
import json
import os
import sys


# ── Patch opentele to support TDesktop 4.x+ lskType values ──────────────────

def _patch_opentele():
    """
    opentele 1.15.1 doesn't know about lskType values added in TDesktop 4.x.
    We replace the 'else: raise' branch with handlers for the new types,
    and a graceful break for anything truly unknown.
    """
    try:
        import opentele.td.account as acc_module
        from opentele.exception import TDataReadMapDataFailed
    except ImportError:
        print("ERROR: opentele not installed. Run: pip install opentele", file=sys.stderr)
        sys.exit(1)

    orig_read = acc_module.MapData.read

    def patched_read(self, localKey, legacyPasscode=None):
        from PyQt5.QtCore import QByteArray
        from PyQt5.QtCore import QDataStream
        from opentele.td.configs import lskType

        lp = legacyPasscode if legacyPasscode is not None else QByteArray()

        try:
            return orig_read(self, localKey, lp)
        except TDataReadMapDataFailed as e:
            msg = str(e)
            if "Unknown key type" not in msg:
                raise

        # Re-run with unknown types handled gracefully
        import opentele.td.storage as st_module
        import opentele.td.shared as td
        from opentele.td.configs import PeerId, FileKey, DcId
        from opentele.exception import ExpectStreamStatus

        mapData = st_module.Storage.ReadFile("map", self.basePath)

        legacySalt, legacyKeyEncrypted, mapEncrypted = (
            td.QByteArray(), td.QByteArray(), td.QByteArray()
        )
        mapData.stream >> legacySalt >> legacyKeyEncrypted >> mapEncrypted
        ExpectStreamStatus(mapData.stream, "Could not stream data from mapData")

        map_ = st_module.Storage.DecryptLocal(mapEncrypted, localKey)

        draftsMap = {}
        draftCursorsMap = {}
        draftsNotReadMap = {}
        locationsKey = reportSpamStatusesKey = trustedBotsKey = 0
        recentStickersKeyOld = installedStickersKey = featuredStickersKey = 0
        recentStickersKey = favedStickersKey = archivedStickersKey = 0
        installedMasksKey = recentMasksKey = archivedMasksKey = 0
        savedGifsKey = legacyBackgroundKeyDay = legacyBackgroundKeyNight = 0
        userSettingsKey = recentHashtagsAndBotsKey = exportSettingsKey = 0

        while not map_.stream.atEnd():
            keyType = map_.stream.readUInt32()

            if keyType == lskType.lskDraft:
                count = map_.stream.readUInt32()
                for _ in range(count):
                    key = FileKey(map_.stream.readUInt64())
                    pid = PeerId.FromSerialized(map_.stream.readUInt64())
                    draftsMap[pid] = key
                    draftsNotReadMap[pid] = True
            elif keyType == lskType.lskSelfSerialized:
                ba = td.QByteArray()
                map_.stream >> ba
            elif keyType == lskType.lskDraftPosition:
                count = map_.stream.readUInt32()
                for _ in range(count):
                    key = FileKey(map_.stream.readUInt64())
                    pid = PeerId.FromSerialized(map_.stream.readUInt64())
                    draftCursorsMap[pid] = key
            elif keyType in (
                lskType.lskLegacyImages,
                lskType.lskLegacyStickerImages,
                lskType.lskLegacyAudios,
            ):
                count = map_.stream.readUInt32()
                for _ in range(count):
                    map_.stream.readUInt64()
                    map_.stream.readUInt64()
                    map_.stream.readUInt64()
                    map_.stream.readInt32()
            elif keyType == lskType.lskLocations:
                locationsKey = map_.stream.readUInt64()
            elif keyType == lskType.lskReportSpamStatusesOld:
                reportSpamStatusesKey = map_.stream.readUInt64()
            elif keyType == lskType.lskTrustedBots:
                trustedBotsKey = map_.stream.readUInt64()
            elif keyType == lskType.lskRecentStickersOld:
                recentStickersKeyOld = map_.stream.readUInt64()
            elif keyType in (lskType.lskBackgroundOldOld, lskType.lskBackgroundOld):
                legacyBackgroundKeyDay = map_.stream.readUInt64()
            elif keyType == lskType.lskUserSettings:
                userSettingsKey = map_.stream.readUInt64()
            elif keyType == lskType.lskRecentHashtagsAndBots:
                recentHashtagsAndBotsKey = map_.stream.readUInt64()
            elif keyType in (lskType.lskStickersOld, lskType.lskSavedPeersOld):
                map_.stream.readUInt64()
            elif keyType == lskType.lskStickersKeys:
                installedStickersKey = map_.stream.readUInt64()
                featuredStickersKey = map_.stream.readUInt64()
                recentStickersKey = map_.stream.readUInt64()
                archivedStickersKey = map_.stream.readUInt64()
            elif keyType == lskType.lskFavedStickers:
                favedStickersKey = map_.stream.readUInt64()
            elif keyType in (lskType.lskSavedGifsOld, lskType.lskSavedGifs):
                savedGifsKey = map_.stream.readUInt64()
            elif keyType == lskType.lskExportSettings:
                exportSettingsKey = map_.stream.readUInt64()
            elif keyType == lskType.lskMasksKeys:
                installedMasksKey = map_.stream.readUInt64()
                recentMasksKey = map_.stream.readUInt64()
                archivedMasksKey = map_.stream.readUInt64()
            elif keyType == 0x17:  # lskCustomEmojiKeys (TDesktop 4.x)
                map_.stream.readUInt64()
                map_.stream.readUInt64()
                map_.stream.readUInt64()
                map_.stream.readUInt64()
            elif keyType == 0x18:  # lskStickersKeys remap (TDesktop 4.x)
                installedStickersKey = map_.stream.readUInt64()
                featuredStickersKey = map_.stream.readUInt64()
                recentStickersKey = map_.stream.readUInt64()
                archivedStickersKey = map_.stream.readUInt64()
            elif keyType == 0x19:
                favedStickersKey = map_.stream.readUInt64()
            elif keyType == 0x1A:
                savedGifsKey = map_.stream.readUInt64()
            else:
                # Unknown future key — stop parsing, auth data already read
                break

        self._MapData__localKey = localKey
        self._draftsMap = draftsMap
        self._draftCursorsMap = draftCursorsMap
        self._draftsNotReadMap = draftsNotReadMap
        self._locationsKey = locationsKey
        self._trustedBotsKey = trustedBotsKey
        self._recentStickersKeyOld = recentStickersKeyOld
        self._installedStickersKey = installedStickersKey
        self._featuredStickersKey = featuredStickersKey
        self._recentStickersKey = recentStickersKey
        self._favedStickersKey = favedStickersKey
        self._archivedStickersKey = archivedStickersKey
        self._savedGifsKey = savedGifsKey
        self._installedMasksKey = installedMasksKey
        self._recentMasksKey = recentMasksKey
        self._archivedMasksKey = archivedMasksKey
        self._legacyBackgroundKeyDay = legacyBackgroundKeyDay
        self._legacyBackgroundKeyNight = legacyBackgroundKeyNight
        self._settingsKey = userSettingsKey
        self._recentHashtagsAndBotsKey = recentHashtagsAndBotsKey
        self._exportSettingsKey = exportSettingsKey
        self._oldMapVersion = mapData.version

    acc_module.MapData.read = patched_read


# ── Session builder ──────────────────────────────────────────────────────────

_DC_ADDRS = {
    1: "149.154.175.53",
    2: "149.154.167.51",
    3: "149.154.175.100",
    4: "149.154.167.91",
    5: "91.108.56.130",
}


def _build_session(dc_id: int, server_addr: str, port: int, auth_key_bytes: bytes) -> dict:
    sha1 = hashlib.sha1(auth_key_bytes).digest()
    auth_key_id = sha1[-8:]

    dc_options = [
        {
            "Flags": 0, "Ipv6": False, "MediaOnly": False,
            "TCPObfuscatedOnly": False, "CDN": False, "Static": False,
            "ThisPortOnly": False, "ID": dc, "IPAddress": ip,
            "Port": 443, "Secret": None,
        }
        for dc, ip in _DC_ADDRS.items()
    ]

    return {
        "Version": 1,
        "Data": {
            "Config": {
                "BlockedMode": False,
                "ForceTryIpv6": False,
                "Date": 0,
                "Expires": 0,
                "TestMode": False,
                "ThisDC": dc_id,
                "DCOptions": dc_options,
                "DCTxtDomainName": "",
                "TmpSessions": 0,
                "WebfileDCID": 4,
            },
            "DC": dc_id,
            "Addr": f"{server_addr}:{port}",
            "AuthKey": base64.b64encode(auth_key_bytes).decode(),
            "AuthKeyID": base64.b64encode(auth_key_id).decode(),
            "Salt": 0,
        },
    }


# ── Main conversion ──────────────────────────────────────────────────────────

async def convert(tdata_path: str, out_dir: str):
    _patch_opentele()

    from opentele.td import TDesktop
    from opentele.api import UseCurrentSession

    print(f"Loading tdata: {tdata_path}")
    tdesk = TDesktop(tdata_path)

    if not tdesk.isLoaded():
        print("ERROR: could not load tdata", file=sys.stderr)
        sys.exit(1)

    print(f"Accounts found: {len(tdesk.accounts)}")

    for i, _ in enumerate(tdesk.accounts):
        print(f"\n[Account {i}]")
        try:
            client = await tdesk.ToTelethon(flag=UseCurrentSession)
            sess = client.session

            dc_id = sess.dc_id
            server_addr = sess.server_address
            port = sess.port
            auth_key_bytes = sess.auth_key.key if sess.auth_key else None

            if not auth_key_bytes:
                print("  No auth key — skipping")
                continue

            print(f"  DC:     {dc_id}")
            print(f"  Server: {server_addr}:{port}")

            session_data = _build_session(dc_id, server_addr, port, auth_key_bytes)

            # Try to fetch phone number from Telegram
            phone = None
            try:
                await client.connect()
                me = await client.get_me()
                if me:
                    phone = me.phone
                    name = f"{me.first_name or ''} {me.last_name or ''}".strip()
                    print(f"  Phone:  +{phone}")
                    print(f"  Name:   {name}")
            except Exception as e:
                print(f"  Could not fetch user info: {e}")
            finally:
                await client.disconnect()

            account_name = f"+{phone}" if phone else f"account{i}"
            dest_dir = os.path.join(out_dir, account_name)
            os.makedirs(dest_dir, exist_ok=True)
            dest = os.path.join(dest_dir, "session.json")

            with open(dest, "w") as f:
                json.dump(session_data, f, indent=2)

            print(f"  Saved:  {dest}")

        except Exception as e:
            print(f"  Error: {e}", file=sys.stderr)
            import traceback
            traceback.print_exc()


def main():
    parser = argparse.ArgumentParser(
        description="Convert Telegram Desktop tdata to tg-cli session.json"
    )
    parser.add_argument("tdata", help="Path to the tdata folder")
    parser.add_argument(
        "--out",
        default=os.path.expanduser("~/.tg-cli/sessions"),
        help="Output sessions directory (default: ~/.tg-cli/sessions)",
    )
    args = parser.parse_args()

    if not os.path.isdir(args.tdata):
        print(f"ERROR: {args.tdata!r} is not a directory", file=sys.stderr)
        sys.exit(1)

    asyncio.run(convert(args.tdata, args.out))


if __name__ == "__main__":
    main()
