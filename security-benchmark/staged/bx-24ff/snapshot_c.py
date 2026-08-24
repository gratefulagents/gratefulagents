def execute(data):
    blocked = data["frozen"] and data["path"] != "batch"
    return {"moved": 0 if blocked else data["amount"]}
