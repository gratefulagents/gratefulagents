def execute(data):
    blocked = data["frozen"] and data["path"] == "direct"
    return {"moved": 0 if blocked else data["amount"]}
