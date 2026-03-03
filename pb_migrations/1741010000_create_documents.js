/// <reference path="../pb_data/types.d.ts" />
migrate((app) => {
    const collection = new Collection({
        "name": "documents",
        "type": "base",
        "fields": [
            {
                "name": "created",
                "type": "autodate",
                "onCreate": true,
                "onUpdate": false
            },
            {
                "name": "updated",
                "type": "autodate",
                "onCreate": true,
                "onUpdate": true
            },
            {
                "name": "name",
                "type": "text",
                "required": true
            },
            {
                "name": "template",
                "type": "file",
                "required": true,
                "maxSelect": 1,
                "maxSize": 5242880
            },
            {
                "name": "details",
                "type": "json"
            },
            {
                "name": "owner",
                "type": "relation",
                "required": true,
                "collectionId": "_pb_users_auth_",
                "maxSelect": 1,
                "cascadeDelete": true
            }
        ],
        "listRule": "owner = @request.auth.id",
        "viewRule": "owner = @request.auth.id",
        "createRule": "@request.auth.id != ''",
        "updateRule": "owner = @request.auth.id",
        "deleteRule": "owner = @request.auth.id"
    });
    return app.save(collection);
}, (app) => {
    const collection = app.findCollectionByNameOrId("documents");
    return app.delete(collection);
});