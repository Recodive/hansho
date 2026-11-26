using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace Hansho;

// The readers and writers of the generated types (see Generated.cs) are built from these. Reading is lenient on
// purpose: a missing or differently typed field reads as absent (empty for a required one), never as an exception.
internal static partial class Wire
{
    /// <summary>Template and activity data are sent as given: no key is renamed and non-ASCII text is not escaped.</summary>
    internal static readonly JsonSerializerOptions DataOptions = new()
    {
        Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
    };

    // --- reading -------------------------------------------------------------

    private static bool Get(JsonElement o, string key, out JsonElement v)
    {
        v = default;
        return o.ValueKind == JsonValueKind.Object && o.TryGetProperty(key, out v);
    }

    internal static string Str(JsonElement o, string key) => OptStr(o, key) ?? "";

    internal static string? OptStr(JsonElement o, string key) =>
        Get(o, key, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

    internal static long Long(JsonElement o, string key) => OptLong(o, key) ?? 0;

    internal static long? OptLong(JsonElement o, string key) => Get(o, key, out var v) ? ToLong(v) : null;

    internal static int Int(JsonElement o, string key) => OptInt(o, key) ?? 0;

    internal static int? OptInt(JsonElement o, string key) => OptLong(o, key) is { } l ? Clamp(l) : null;

    internal static bool Bool(JsonElement o, string key) => OptBool(o, key) ?? false;

    internal static bool? OptBool(JsonElement o, string key) =>
        Get(o, key, out var v) && v.ValueKind is JsonValueKind.True or JsonValueKind.False ? v.GetBoolean() : null;

    internal static DateTimeOffset? Time(JsonElement o, string key) =>
        OptStr(o, key) is { } s && DateTimeOffset.TryParse(s, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t) ? t : null;

    /// <summary>The value as it is, copied out of its document; null when absent or null.</summary>
    internal static object? Any(JsonElement o, string key) =>
        Get(o, key, out var v) && v.ValueKind != JsonValueKind.Null ? v.Clone() : null;

    /// <summary>A nested object; an absent or differently typed one reads as an empty object.</summary>
    internal static JsonElement Sub(JsonElement o, string key) =>
        Get(o, key, out var v) && v.ValueKind == JsonValueKind.Object ? v : default;

    internal static T? OptObj<T>(JsonElement o, string key, Func<JsonElement, T> read) where T : class =>
        Get(o, key, out var v) && v.ValueKind == JsonValueKind.Object ? read(v) : null;

    internal static T? OptEnum<T>(JsonElement o, string key, Func<string, T> make) where T : struct =>
        OptStr(o, key) is { } s ? make(s) : null;

    /// <summary>The elements of an array that have the given kind (any kind for Undefined); null when it is absent.</summary>
    internal static IReadOnlyList<T>? Items<T>(JsonElement o, string key, JsonValueKind kind, Func<JsonElement, T> read) =>
        Get(o, key, out var v) ? ItemsOf(v, kind, read) : null;

    /// <summary>Like <c>Items</c> for an array that is the whole answer; empty when it is not an array.</summary>
    internal static IReadOnlyList<T> ListOf<T>(JsonElement array, JsonValueKind kind, Func<JsonElement, T> read) =>
        ItemsOf(array, kind, read) ?? Array.Empty<T>();

    private static IReadOnlyList<T>? ItemsOf<T>(JsonElement array, JsonValueKind kind, Func<JsonElement, T> read)
    {
        if (array.ValueKind != JsonValueKind.Array) return null;
        var list = new List<T>();
        foreach (var e in array.EnumerateArray())
            if (kind == JsonValueKind.Undefined || e.ValueKind == kind) list.Add(read(e));
        return list;
    }

    internal static int AsInt(JsonElement e) => Clamp(ToLong(e) ?? 0);

    internal static long AsLong(JsonElement e) => ToLong(e) ?? 0;

    private static int Clamp(long l) => (int)Math.Clamp(l, int.MinValue, int.MaxValue);

    // A number, or a string holding one; anything else is not a number.
    private static long? ToLong(JsonElement v)
    {
        if (v.ValueKind == JsonValueKind.Number) return v.TryGetInt64(out var l) ? l : (long)v.GetDouble();
        return v.ValueKind == JsonValueKind.String && double.TryParse(v.GetString(), NumberStyles.Float, CultureInfo.InvariantCulture, out var d) ? (long)d : null;
    }

    // --- writing -------------------------------------------------------------

    internal static void Put(JsonObject o, string key, string? value)
    {
        if (value is not null) o[key] = value;
    }

    internal static JsonArray Arr<T>(IEnumerable<T> items, Func<T, JsonNode?> write)
    {
        var arr = new JsonArray();
        foreach (var item in items) arr.Add(write(item)); // not Add<T>: that needs reflection-based serialization
        return arr;
    }

    internal static string Iso(DateTimeOffset t) => t.UtcDateTime.ToString("yyyy-MM-dd'T'HH:mm:ss.FFFFFFF'Z'", CultureInfo.InvariantCulture);

    internal static JsonNode? ToNode(object? value) => value switch
    {
        null => null,
        JsonNode n => n.DeepClone(),
        JsonElement e => JsonSerializer.SerializeToNode(e, DataOptions),
        _ => JsonSerializer.SerializeToNode(value, value.GetType(), DataOptions),
    };
}
