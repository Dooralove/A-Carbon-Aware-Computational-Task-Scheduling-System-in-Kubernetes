"""One-shot generator + stdlib validator for carbon-intensity replay examples."""

from __future__ import annotations

import json
import math
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCHEMAS = ROOT / "experiments" / "datasets" / "schemas"
EXAMPLES = ROOT / "experiments" / "datasets" / "examples"
UTC = timezone.utc
START = datetime(2026, 9, 1, tzinfo=UTC)
DATE_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$"
)
ZONE_RE = re.compile(r"^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$")


def rfc3339(dt: datetime) -> str:
    return dt.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def solar_fraction(hour: float) -> float:
    return max(0.0, math.cos((hour - 12.0) * math.pi / 12.0))


def wind_fraction(hour: float) -> float:
    return 0.5 + 0.5 * math.sin((hour - 3.0) * 2.0 * math.pi / 24.0)


def round_ci(value: float) -> float:
    return round(value, 1)


def stats(values: list[float]) -> tuple[float, float]:
    mean = sum(values) / len(values)
    var = sum((x - mean) ** 2 for x in values) / len(values)
    cv = (var**0.5) / mean if mean else 0.0
    steps = [abs(values[i] - values[i - 1]) for i in range(1, len(values))]
    return cv, max(steps) if steps else 0.0


def ranking_reversals(by_ts: dict[str, dict[str, float]]) -> int:
    prev = None
    reversals = 0
    for ts in sorted(by_ts):
        winner = min(by_ts[ts], key=by_ts[ts].get)
        if prev is not None and winner != prev:
            reversals += 1
        prev = winner
    return reversals


def dump(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def make_stable() -> dict:
    hours = 24
    series = []
    by_ts: dict[str, dict[str, float]] = {}
    params = {"north": (85.0, 3.0), "central": (240.0, 5.0), "west": (410.0, 6.0)}
    for h in range(hours):
        ts = START + timedelta(hours=h)
        key = rfc3339(ts)
        by_ts[key] = {}
        for zone, (mu, amp) in params.items():
            ci = round_ci(mu + amp * math.sin(2.0 * math.pi * h / 24.0))
            series.append({"zone": zone, "timestamp": key, "carbonIntensity": ci, "isEstimated": False})
            by_ts[key][zone] = ci
    cvs = []
    steps = []
    for zone in params:
        vals = [p["carbonIntensity"] for p in series if p["zone"] == zone]
        cv, step = stats(vals)
        cvs.append(cv)
        steps.append(step)
    return {
        "schemaVersion": "1.0.0",
        "datasetId": "synthetic_multizone_stable_24h_v1",
        "source": "synthetic",
        "scenario": "stable",
        "description": "Low-variability 60-minute series. Zone ranking north < central < west is constant.",
        "unit": "gCO2e/kWh",
        "emissionFactorType": "lifecycle",
        "flowTraced": True,
        "temporalGranularity": "60_minutes",
        "intervalSeconds": 3600,
        "timeBase": "model",
        "modelStart": rfc3339(START),
        "modelEnd": rfc3339(START + timedelta(hours=hours)),
        "seed": 1,
        "forecastMode": "persistence",
        "forecastHorizonSeconds": 43200,
        "zones": [
            {"zone": "north", "sourceZone": "NO-NO1", "label": "hydro-like stable low CI"},
            {"zone": "central", "sourceZone": "FR", "label": "nuclear-like stable mid CI"},
            {"zone": "west", "sourceZone": "PL", "label": "fossil-like stable high CI"},
        ],
        "variability": {
            "coefficientOfVariationMin": round(min(cvs), 4),
            "coefficientOfVariationMax": round(max(cvs), 4),
            "maxStepGco2ePerKwh": round(max(steps), 1),
            "rankingReversals": ranking_reversals(by_ts),
        },
        "series": series,
    }


def make_dynamic() -> dict:
    hours = 24
    series = []
    by_ts: dict[str, dict[str, float]] = {}
    for h in range(hours):
        ts = START + timedelta(hours=h)
        key = rfc3339(ts)
        s = solar_fraction(float(h))
        w = wind_fraction(float(h))
        values = {
            "north": round_ci(90.0 + 50.0 * s - 40.0 * w),
            "central": round_ci(280.0 - 200.0 * s - 20.0 * w),
            "west": round_ci(400.0 - 40.0 * s - 30.0 * w),
        }
        by_ts[key] = values
        for zone, ci in values.items():
            series.append({"zone": zone, "timestamp": key, "carbonIntensity": ci, "isEstimated": False})
    cvs = []
    steps = []
    for zone in ("north", "central", "west"):
        vals = [p["carbonIntensity"] for p in series if p["zone"] == zone]
        cv, step = stats(vals)
        cvs.append(cv)
        steps.append(step)
    return {
        "schemaVersion": "1.0.0",
        "datasetId": "synthetic_multizone_dynamic_24h_v1",
        "source": "synthetic",
        "scenario": "dynamic",
        "description": "Diurnal solar/wind cycle. Night: north best (wind). Midday: central best (solar).",
        "unit": "gCO2e/kWh",
        "emissionFactorType": "lifecycle",
        "flowTraced": True,
        "temporalGranularity": "60_minutes",
        "intervalSeconds": 3600,
        "timeBase": "model",
        "modelStart": rfc3339(START),
        "modelEnd": rfc3339(START + timedelta(hours=hours)),
        "seed": 2,
        "forecastMode": "persistence",
        "forecastHorizonSeconds": 43200,
        "zones": [
            {"zone": "north", "sourceZone": "GB", "label": "wind-dominated, lower CI at night"},
            {"zone": "central", "sourceZone": "ES", "label": "solar-dominated, midday valley"},
            {"zone": "west", "sourceZone": "DE", "label": "fossil baseline with weak solar dip"},
        ],
        "variability": {
            "coefficientOfVariationMin": round(min(cvs), 4),
            "coefficientOfVariationMax": round(max(cvs), 4),
            "maxStepGco2ePerKwh": round(max(steps), 1),
            "rankingReversals": ranking_reversals(by_ts),
        },
        "series": series,
    }


def make_volatile() -> dict:
    window_start = START + timedelta(hours=10)
    n = 24
    series = []
    by_ts: dict[str, dict[str, float]] = {}

    def profile(minute: int) -> dict[str, float]:
        if minute < 25:
            return {"north": 85.0, "central": 260.0, "west": 410.0}
        if minute < 60:
            return {"north": 420.0, "central": 250.0, "west": 70.0}
        if minute < 95:
            return {"north": 90.0, "central": 400.0, "west": 380.0}
        return {"north": 380.0, "central": 70.0, "west": 410.0}

    for i in range(n):
        ts = window_start + timedelta(minutes=5 * i)
        key = rfc3339(ts)
        minute = 10 * 60 + 5 * i
        minute_of_window = 5 * i
        values = profile(minute_of_window)
        by_ts[key] = values
        for zone, ci in values.items():
            series.append({"zone": zone, "timestamp": key, "carbonIntensity": ci, "isEstimated": False})
        _ = minute
    cvs = []
    steps = []
    for zone in ("north", "central", "west"):
        vals = [p["carbonIntensity"] for p in series if p["zone"] == zone]
        cv, step = stats(vals)
        cvs.append(cv)
        steps.append(step)
    snapshots = []
    issued = window_start
    for zone in ("north", "central", "west"):
        forecast = []
        for i in range(12):
            ts = issued + timedelta(minutes=5 * (i + 1))
            minute_of_window = 5 * (i + 1)
            ci = profile(minute_of_window)[zone]
            forecast.append(
                {
                    "timestamp": rfc3339(ts),
                    "carbonIntensity": ci,
                    "horizonSeconds": 300 * (i + 1),
                }
            )
        snapshots.append({"zone": zone, "issuedAt": rfc3339(issued), "forecast": forecast})
    return {
        "schemaVersion": "1.0.0",
        "datasetId": "synthetic_multizone_volatile_2h_v1",
        "source": "synthetic",
        "scenario": "volatile",
        "description": "5-minute series with step changes of hundreds of gCO2e/kWh and three ranking reversals.",
        "unit": "gCO2e/kWh",
        "emissionFactorType": "lifecycle",
        "flowTraced": True,
        "temporalGranularity": "5_minutes",
        "intervalSeconds": 300,
        "timeBase": "model",
        "modelStart": rfc3339(window_start),
        "modelEnd": rfc3339(window_start + timedelta(hours=2)),
        "seed": 3,
        "forecastMode": "recorded",
        "forecastHorizonSeconds": 3600,
        "zones": [
            {"zone": "north", "label": "volatile ranking participant"},
            {"zone": "central", "label": "volatile ranking participant"},
            {"zone": "west", "label": "volatile ranking participant"},
        ],
        "variability": {
            "coefficientOfVariationMin": round(min(cvs), 4),
            "coefficientOfVariationMax": round(max(cvs), 4),
            "maxStepGco2ePerKwh": round(max(steps), 1),
            "rankingReversals": ranking_reversals(by_ts),
        },
        "series": series,
        "forecastSnapshots": snapshots,
    }


def type_ok(value, expected: str) -> bool:
    if expected == "string":
        return isinstance(value, str)
    if expected == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if expected == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)
    if expected == "boolean":
        return isinstance(value, bool)
    if expected == "array":
        return isinstance(value, list)
    if expected == "object":
        return isinstance(value, dict)
    if expected == "null":
        return value is None
    return False


def check_format(fmt: str, value: str) -> bool:
    if fmt != "date-time":
        return True
    if DATE_RE.match(value):
        return True
    try:
        datetime.fromisoformat(value.replace("Z", "+00:00"))
        return True
    except ValueError:
        return False


def validate(instance, schema: dict, path: str, errors: list[str]) -> None:
    if "const" in schema and instance != schema["const"]:
        errors.append(f"{path}: expected const {schema['const']!r}, got {instance!r}")
        return
    if "enum" in schema and instance not in schema["enum"]:
        errors.append(f"{path}: {instance!r} not in enum {schema['enum']}")
        return
    expected = schema.get("type")
    if isinstance(expected, list):
        if not any(type_ok(instance, t) for t in expected):
            errors.append(f"{path}: type {type(instance).__name__} not in {expected}")
            return
    elif expected and not type_ok(instance, expected):
        errors.append(f"{path}: expected {expected}, got {type(instance).__name__}")
        return
    if isinstance(instance, str):
        if "pattern" in schema and not re.match(schema["pattern"], instance):
            errors.append(f"{path}: {instance!r} does not match {schema['pattern']}")
        if "format" in schema and not check_format(schema["format"], instance):
            errors.append(f"{path}: {instance!r} is not {schema['format']}")
        if "minLength" in schema and len(instance) < schema["minLength"]:
            errors.append(f"{path}: string shorter than {schema['minLength']}")
        if "maxLength" in schema and len(instance) > schema["maxLength"]:
            errors.append(f"{path}: string longer than {schema['maxLength']}")
    if isinstance(instance, (int, float)) and not isinstance(instance, bool):
        if "minimum" in schema and instance < schema["minimum"]:
            errors.append(f"{path}: {instance} < minimum {schema['minimum']}")
        if "maximum" in schema and instance > schema["maximum"]:
            errors.append(f"{path}: {instance} > maximum {schema['maximum']}")
    if isinstance(instance, list) and "items" in schema:
        if "minItems" in schema and len(instance) < schema["minItems"]:
            errors.append(f"{path}: array shorter than {schema['minItems']}")
        if "maxItems" in schema and len(instance) > schema["maxItems"]:
            errors.append(f"{path}: array longer than {schema['maxItems']}")
        for i, item in enumerate(instance):
            validate(item, schema["items"], f"{path}[{i}]", errors)
    if isinstance(instance, dict) and schema.get("type") == "object" or (
        isinstance(instance, dict) and "properties" in schema
    ):
        required = schema.get("required", [])
        for key in required:
            if key not in instance:
                errors.append(f"{path}: missing required {key}")
        props = schema.get("properties", {})
        additional = schema.get("additionalProperties", True)
        for key, value in instance.items():
            if key in props:
                validate(value, props[key], f"{path}.{key}", errors)
            elif additional is False:
                errors.append(f"{path}: unexpected property {key}")
    if "oneOf" in schema:
        matches = 0
        for sub in schema["oneOf"]:
            nested: list[str] = []
            validate(instance, sub, path, nested)
            if not nested:
                matches += 1
        if matches != 1:
            errors.append(f"{path}: oneOf matched {matches} alternatives")
    if "$ref" in schema and "properties" not in schema and schema.get("type") != "object":
        errors.append(f"{path}: unresolved $ref {schema['$ref']}")


def extra_replay_checks(data: dict, errors: list[str]) -> None:
    if data["unit"] != "gCO2e/kWh":
        errors.append("unit must be gCO2e/kWh")
    gran_map = {"5_minutes": 300, "15_minutes": 900, "30_minutes": 1800, "60_minutes": 3600}
    if data["intervalSeconds"] != gran_map[data["temporalGranularity"]]:
        errors.append("intervalSeconds does not match temporalGranularity")
    start = datetime.fromisoformat(data["modelStart"].replace("Z", "+00:00"))
    end = datetime.fromisoformat(data["modelEnd"].replace("Z", "+00:00"))
    if not (end > start):
        errors.append("modelEnd must be after modelStart")
    seen = set()
    zones = {z["zone"] for z in data["zones"]}
    for i, point in enumerate(data["series"]):
        key = (point["zone"], point["timestamp"])
        if key in seen:
            errors.append(f"duplicate series point {key}")
        seen.add(key)
        if point["zone"] not in zones:
            errors.append(f"series[{i}] zone {point['zone']} not in zones")
        if point["carbonIntensity"] < 0:
            errors.append(f"series[{i}] negative CI")
    expected_count = int((end - start).total_seconds() // data["intervalSeconds"]) * len(zones)
    if len(data["series"]) != expected_count:
        errors.append(f"series length {len(data['series'])} != expected {expected_count}")
    if data["forecastMode"] == "recorded" and not data.get("forecastSnapshots"):
        errors.append("recorded forecastMode requires forecastSnapshots")
    if data["forecastMode"] != "recorded" and data.get("forecastSnapshots"):
        errors.append("forecastSnapshots present but forecastMode is not recorded")
    var = data.get("variability", {})
    scenario = data["scenario"]
    if scenario == "stable":
        if var.get("coefficientOfVariationMax", 1) >= 0.05:
            errors.append("stable: CV max must be < 0.05")
        if var.get("maxStepGco2ePerKwh", 99) >= 10:
            errors.append("stable: max step must be < 10 gCO2e/kWh")
        if var.get("rankingReversals", 1) != 0:
            errors.append("stable: ranking must not reverse")
    if scenario == "dynamic":
        if var.get("coefficientOfVariationMax", 0) < 0.15:
            errors.append("dynamic: CV max (most variable zone) must be >= 0.15")
        if var.get("rankingReversals", 0) < 1:
            errors.append("dynamic: at least one ranking reversal required")
    if scenario == "volatile":
        if var.get("maxStepGco2ePerKwh", 0) < 300:
            errors.append("volatile: max step must be >= 300 gCO2e/kWh")
        if var.get("rankingReversals", 0) < 2:
            errors.append("volatile: at least two ranking reversals required")
        if data["temporalGranularity"] != "5_minutes":
            errors.append("volatile fixture must be 5_minutes")


def extra_live_current_checks(data: dict, errors: list[str]) -> None:
    if data["unit"] != "gCO2e/kWh":
        errors.append("live current unit must be gCO2e/kWh")
    if data["carbonIntensity"] < 0:
        errors.append("negative carbonIntensity")
    if data["freshnessSeconds"] < 0:
        errors.append("negative freshnessSeconds")


def extra_live_forecast_checks(data: dict, errors: list[str]) -> None:
    if data["unit"] != "gCO2e/kWh":
        errors.append("live forecast unit must be gCO2e/kWh")
    prev = None
    issued = datetime.fromisoformat(data["issuedAt"].replace("Z", "+00:00"))
    for i, point in enumerate(data["forecast"]):
        ts = datetime.fromisoformat(point["timestamp"].replace("Z", "+00:00"))
        if prev is not None and ts <= prev:
            errors.append(f"forecast[{i}] not strictly increasing")
        prev = ts
        if "horizonSeconds" in point:
            expected = int((ts - issued).total_seconds())
            if point["horizonSeconds"] != expected:
                errors.append(f"forecast[{i}] horizonSeconds {point['horizonSeconds']} != {expected}")


def main() -> int:
    EXAMPLES.mkdir(parents=True, exist_ok=True)
    datasets = {
        "synthetic_multizone_stable_24h_v1.json": make_stable(),
        "synthetic_multizone_dynamic_24h_v1.json": make_dynamic(),
        "synthetic_multizone_volatile_2h_v1.json": make_volatile(),
    }
    live_current = {
        "zone": "north",
        "carbonIntensity": 82.4,
        "unit": "gCO2e/kWh",
        "timestamp": "2026-09-01T10:00:00Z",
        "source": "electricity-maps",
        "sourceZone": "NO-NO1",
        "freshnessSeconds": 35,
        "quality": "fresh",
        "emissionFactorType": "lifecycle",
        "temporalGranularity": "5_minutes",
        "intervalSeconds": 300,
        "carbonFreePercentage": 72.0,
        "isEstimated": False,
        "estimationMethod": None,
        "flowTraced": True,
        "fetchedAt": "2026-09-01T10:00:35Z",
        "updatedAt": "2026-09-01T10:00:12Z",
        "expiresAt": "2026-09-01T10:05:00Z",
    }
    live_forecast = {
        "zone": "north",
        "unit": "gCO2e/kWh",
        "issuedAt": "2026-09-01T10:00:00Z",
        "source": "electricity-maps",
        "emissionFactorType": "lifecycle",
        "temporalGranularity": "5_minutes",
        "forecastMode": "recorded",
        "freshnessSeconds": 35,
        "forecast": [
            {"timestamp": "2026-09-01T10:05:00Z", "carbonIntensity": 84.0, "horizonSeconds": 300},
            {"timestamp": "2026-09-01T10:10:00Z", "carbonIntensity": 91.2, "horizonSeconds": 600},
            {"timestamp": "2026-09-01T10:15:00Z", "carbonIntensity": 88.5, "horizonSeconds": 900},
        ],
    }
    em_latest = {
        "zone": "DK-DK2",
        "carbonIntensity": 318,
        "datetime": "2024-05-19T03:00:00.000Z",
        "updatedAt": "2025-07-25T12:32:42.039Z",
        "createdAt": "2024-05-16T03:48:50.419Z",
        "emissionFactorType": "lifecycle",
        "flowTraced": True,
        "isEstimated": False,
        "estimationMethod": "FORECASTS_HIERARCHY",
        "temporalGranularity": "hourly",
    }
    neso = {
        "data": [
            {
                "from": "2018-01-20T12:00Z",
                "to": "2018-01-20T12:30Z",
                "intensity": {"forecast": 266, "actual": 263, "index": "moderate"},
            }
        ]
    }

    dump(EXAMPLES / "live_current.example.json", live_current)
    dump(EXAMPLES / "live_forecast.example.json", live_forecast)
    dump(EXAMPLES / "electricity_maps_latest.example.json", em_latest)
    dump(EXAMPLES / "neso_intensity.example.json", neso)

    errors: list[str] = []
    replay_schema = json.loads((SCHEMAS / "replay_dataset.schema.json").read_text(encoding="utf-8"))
    current_schema = json.loads((SCHEMAS / "live_current.schema.json").read_text(encoding="utf-8"))
    forecast_schema = json.loads((SCHEMAS / "live_forecast.schema.json").read_text(encoding="utf-8"))
    neso_schema = json.loads((SCHEMAS / "neso.schema.json").read_text(encoding="utf-8"))

    for name, payload in datasets.items():
        dump(EXAMPLES / name, payload)
        local: list[str] = []
        validate(payload, replay_schema, name, local)
        extra_replay_checks(payload, local)
        for err in local:
            errors.append(err)
        var = payload["variability"]
        print(
            f"{name}: points={len(payload['series'])} "
            f"CV=[{var['coefficientOfVariationMin']}, {var['coefficientOfVariationMax']}] "
            f"maxStep={var['maxStepGco2ePerKwh']} gCO2e/kWh "
            f"reversals={var['rankingReversals']}"
        )

    local = []
    validate(live_current, current_schema, "live_current", local)
    extra_live_current_checks(live_current, local)
    errors.extend(local)

    local = []
    validate(live_forecast, forecast_schema, "live_forecast", local)
    extra_live_forecast_checks(live_forecast, local)
    errors.extend(local)

    local = []
    validate(neso, neso_schema, "neso", local)
    errors.extend(local)

    em_errors: list[str] = []
    if em_latest["carbonIntensity"] < 0:
        em_errors.append("EM carbonIntensity negative")
    if em_latest["emissionFactorType"] not in ("lifecycle", "direct"):
        em_errors.append("EM emissionFactorType invalid")
    errors.extend(em_errors)

    if errors:
        print("VALIDATION FAILED")
        for err in errors:
            print(" -", err)
        return 1
    print("VALIDATION OK")
    print("unit canonical: gCO2e/kWh")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
