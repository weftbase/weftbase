-- 0035 · 0034's MetaKV read nothing from a Meta that is not valid UTF-8
-- (the conformance row Strings). The views write a row's Meta with
-- toJSONString, which copies an attribute's bytes as they are, and
-- JSONExtract refuses a document holding a byte that is not UTF-8: the
-- column's DEFAULT filled ([], []) and the run read no metadata at all
-- — its identity kept, every pair (a tenant tag among them) gone. Before
-- 0034 the Go side decoded Meta itself and read such a byte as U+FFFD,
-- as obsdb/sqlite does.
--
-- The DEFAULT now reads toValidUTF8(Meta): an invalid sequence becomes
-- U+FFFD and the rest of the map is kept. A part written before 0034
-- computes MetaKV from the DEFAULT on read and is right at once. The
-- UPDATE repairs the rows written between 0034 and this version with
-- such a Meta: their MetaKV gains the pairs of their own Meta, and the
-- pairs MetaKV already holds (merged in from other rows) win over them;
-- a merge that kept another row's Meta loses what max() dropped, as
-- 0034 says. It is a mutation ClickHouse runs in the background
-- (mutations_sync 0, 0016's rule): until it finishes those runs read as
-- before. Only rows whose Meta is not valid UTF-8 are touched.

ALTER TABLE weft_runs MODIFY COLUMN MetaKV DEFAULT (mapKeys(JSONExtract(toValidUTF8(Meta), 'Map(String, String)')), mapValues(JSONExtract(toValidUTF8(Meta), 'Map(String, String)')));

ALTER TABLE weft_runs UPDATE MetaKV = (mapKeys(mapUpdate(JSONExtract(toValidUTF8(Meta), 'Map(String, String)'), CAST(MetaKV, 'Map(String, String)'))), mapValues(mapUpdate(JSONExtract(toValidUTF8(Meta), 'Map(String, String)'), CAST(MetaKV, 'Map(String, String)'))))
WHERE NOT isValidUTF8(Meta);
