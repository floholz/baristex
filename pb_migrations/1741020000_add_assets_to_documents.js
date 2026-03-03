/// <reference path="../pb_data/types.d.ts" />
migrate((app) => {
    const collection = app.findCollectionByNameOrId("documents");

    collection.fields.add(new Field({
        "name": "assets",
        "type": "file",
        "maxSelect": 99,
        "maxSize": 10485760
    }));

    return app.save(collection);
}, (app) => {
    const collection = app.findCollectionByNameOrId("documents");
    const field = collection.fields.getByName("assets");
    if (field) {
        collection.fields.remove(field.id);
    }
    return app.save(collection);
});