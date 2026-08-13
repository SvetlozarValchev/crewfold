#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

if [ "$#" -ne 1 ] || [ ! -d "$1" ]
then
    printf 'usage: %s OUTPUT_DIRECTORY\n' "$0" >&2
    exit 2
fi
output_dir=$1

# sqlc needs table, view, and index definitions to type-check named queries, but
# it does not use runtime triggers. Large SQLite trigger expressions can make the
# pinned parser pathologically slow, so provide the same ordered migrations with
# complete CREATE TRIGGER statements removed. Runtime migration execution still
# uses the authoritative files byte-for-byte.
find internal/store/migrations -type f -name '*.sql' -print |
    LC_ALL=C sort |
    while IFS= read -r migration
    do
        output_path="$output_dir/$(basename "$migration")"
        awk '
            /^[[:space:]]*CREATE( TEMP| TEMPORARY)? TRIGGER[[:space:]]/ {
                if ($0 !~ /END;[[:space:]]*$/) {
                    in_trigger = 1
                }
                next
            }
            in_trigger && /BEGIN/ && /END;[[:space:]]*$/ {
                in_trigger = 0
                next
            }
            in_trigger && /^[[:space:]]*END;[[:space:]]*$/ {
                in_trigger = 0
                next
            }
            !in_trigger { print }
            END {
                if (in_trigger) {
                    print "unterminated CREATE TRIGGER in " FILENAME > "/dev/stderr"
                    exit 1
                }
            }
        ' "$migration" > "$output_path"
    done

object_manifest() {
    awk '
        function emit_object(    field, kind, name) {
            field = 2
            if ($field == "UNIQUE") field++
            kind = $field
            if (kind == "VIRTUAL") {
                field++
                kind = kind " " $field
            }
            field++
            if ($field == "IF") field += 3
            name = $field
            gsub(/^[`"[]|[`"(].*$/, "", name)
            print kind ":" name
        }
        /^[[:space:]]*CREATE[[:space:]]+(UNIQUE[[:space:]]+)?(VIRTUAL[[:space:]]+)?(TABLE|VIEW|INDEX)[[:space:]]/ {
            emit_object()
        }
    ' "$@" | LC_ALL=C sort -u
}

expected_manifest=$(mktemp)
actual_manifest=$(mktemp)
cleanup() {
    rm -f "$expected_manifest" "$actual_manifest"
}
trap cleanup EXIT HUP INT TERM

# Guard the projection itself: a new compact trigger spelling must never make
# the filter consume the table/view/index definition that follows it.
object_manifest internal/store/migrations/*.sql > "$expected_manifest"
object_manifest "$output_dir"/*.sql > "$actual_manifest"
if ! diff -u "$expected_manifest" "$actual_manifest" >&2
then
    printf 'sqlc schema projection changed a table, view, or index definition\n' >&2
    exit 1
fi
if grep -R -q -E '^[[:space:]]*CREATE( TEMP| TEMPORARY)? TRIGGER[[:space:]]' "$output_dir"
then
    printf 'sqlc schema projection retained a runtime trigger\n' >&2
    exit 1
fi
